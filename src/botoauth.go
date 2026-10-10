package main

// Signing a bot in to a remote MCP server that wants OAuth. Lasso is the OAuth
// client here (oauth.go is the other direction: lasso as a server), following
// the MCP authorization spec: protected-resource metadata (RFC 9728) names the
// authorization server, its metadata (RFC 8414) the endpoints, dynamic client
// registration (RFC 7591) a client, and the code flow with PKCE (S256) and a
// resource indicator (RFC 8707) the tokens.
//
// The redirect comes back to lasso itself (/api/bots/oauth/callback, on the
// origin the human's browser is already using), so on a VPS no localhost is
// involved. A server that only accepts a localhost redirect gets one anyway;
// the browser then fails to load it, and the human pastes the address it
// landed on (POST /api/bots/oauth/finish) — the code and state are in it.
//
// Tokens never sit in lasso.db. The refresh token, client secret and access
// token go to the bot's fnox.toml (its default provider), and claude reads the
// access token through the server's headersHelper, which Claude Code runs on
// every connection and again after a 401. Lasso's loop refreshes it ahead of
// expiry. lasso.db keeps only what is not secret: client id, endpoints,
// expiry, status.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const botOAuthSchema = `
CREATE TABLE IF NOT EXISTS bot_mcp_oauth (
	bot             TEXT NOT NULL,
	server          TEXT NOT NULL,
	issuer          TEXT NOT NULL DEFAULT '',
	token_endpoint  TEXT NOT NULL DEFAULT '',
	client_id       TEXT NOT NULL DEFAULT '',
	auth_method     TEXT NOT NULL DEFAULT '',
	resource        TEXT NOT NULL DEFAULT '',
	scope           TEXT NOT NULL DEFAULT '',
	expires_at      INTEGER NOT NULL DEFAULT 0,
	status          TEXT NOT NULL DEFAULT '',
	error           TEXT NOT NULL DEFAULT '',
	updated_at      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (bot, server)
);
`

// botOAuthHTTP is the client for every request to a server or authorization
// server. A var for tests.
var botOAuthHTTP = &http.Client{Timeout: 15 * time.Second}

// The fnox keys a server's credentials live under. They are lasso's, and are
// never granted to the bot's task as environment (botTaskEnvKeys): the access
// token reaches claude only through headersHelper, the rest never does.
func botOAuthKey(server, part string) string {
	return "LASSO_OAUTH_" + strings.ToUpper(strings.ReplaceAll(server, "-", "_")) + "_" + part
}

const botOAuthKeyPrefix = "LASSO_OAUTH_"

// botOAuthHelper is the headersHelper command for a server: Claude Code runs it
// in a shell and merges the JSON it prints into the request headers.
func botOAuthHelper(dir, server string) string {
	return fmt.Sprintf(`printf '{"Authorization":"Bearer %%s"}' "$(fnox -c %s get %s)"`,
		shellQuote(botFnoxFile(dir)), botOAuthKey(server, "ACCESS"))
}

// --- state --------------------------------------------------------------------

type botOAuthRow struct {
	Bot           string `json:"-"`
	Server        string `json:"server"`
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"-"`
	ClientID      string `json:"client_id"`
	AuthMethod    string `json:"-"`
	Resource      string `json:"-"`
	Scope         string `json:"scope"`
	ExpiresAt     int64  `json:"expires_at"`
	Status        string `json:"status"` // connected | expired | error
	Error         string `json:"error,omitempty"`
}

func getBotOAuth(bot, server string) (*botOAuthRow, error) {
	var r botOAuthRow
	err := db.QueryRow(`SELECT bot, server, issuer, token_endpoint, client_id, auth_method, resource, scope, expires_at, status, error
		FROM bot_mcp_oauth WHERE bot = ? AND server = ?`, bot, server).Scan(
		&r.Bot, &r.Server, &r.Issuer, &r.TokenEndpoint, &r.ClientID, &r.AuthMethod, &r.Resource, &r.Scope, &r.ExpiresAt, &r.Status, &r.Error)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func listBotOAuth(bot string) map[string]*botOAuthRow {
	out := map[string]*botOAuthRow{}
	rows, err := db.Query(`SELECT bot, server, issuer, token_endpoint, client_id, auth_method, resource, scope, expires_at, status, error
		FROM bot_mcp_oauth WHERE bot = ?`, bot)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r botOAuthRow
		if rows.Scan(&r.Bot, &r.Server, &r.Issuer, &r.TokenEndpoint, &r.ClientID, &r.AuthMethod, &r.Resource, &r.Scope, &r.ExpiresAt, &r.Status, &r.Error) == nil {
			out[r.Server] = &r
		}
	}
	return out
}

func saveBotOAuth(r *botOAuthRow) error {
	_, err := db.Exec(`INSERT INTO bot_mcp_oauth (bot, server, issuer, token_endpoint, client_id, auth_method, resource, scope, expires_at, status, error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bot, server) DO UPDATE SET issuer = excluded.issuer, token_endpoint = excluded.token_endpoint,
		client_id = excluded.client_id, auth_method = excluded.auth_method, resource = excluded.resource, scope = excluded.scope,
		expires_at = excluded.expires_at, status = excluded.status, error = excluded.error, updated_at = excluded.updated_at`,
		r.Bot, r.Server, r.Issuer, r.TokenEndpoint, r.ClientID, r.AuthMethod, r.Resource, r.Scope, r.ExpiresAt, r.Status, r.Error,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

func deleteBotOAuth(bot, server string) {
	_, _ = db.Exec(`DELETE FROM bot_mcp_oauth WHERE bot = ? AND server = ?`, bot, server)
}

// pending sign-ins, by state. In memory: one lasts minutes, and a lasso
// restart in the middle just means pressing Sign in again.
type botOAuthPending struct {
	bot, server   string
	verifier      string
	redirect      string
	clientID      string
	clientSecret  string
	authMethod    string
	issuer        string
	tokenEndpoint string
	resource      string
	scope         string
	created       time.Time
}

var botOAuthPend = struct {
	sync.Mutex
	m map[string]*botOAuthPending
}{m: map[string]*botOAuthPending{}}

const botOAuthPendingTTL = 15 * time.Minute

// --- discovery ------------------------------------------------------------------

type authServerMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

var wwwAuthParamRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// discoverAuthServer finds the authorization server for an MCP endpoint and
// the scope to ask for: the server's 401 names its resource metadata
// (WWW-Authenticate resource_metadata=…), else the well-known path is tried,
// else the MCP origin itself is assumed to be the authorization server, which
// is what older servers do.
func discoverAuthServer(mcpURL string) (*authServerMeta, string, error) {
	u, err := url.Parse(mcpURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, "", fmt.Errorf("not an http(s) URL: %q", mcpURL)
	}
	origin := u.Scheme + "://" + u.Host
	var prmURL, scope string
	req, _ := http.NewRequest(http.MethodPost, mcpURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if res, err := botOAuthHTTP.Do(req); err == nil {
		res.Body.Close()
		for _, m := range wwwAuthParamRE.FindAllStringSubmatch(res.Header.Get("WWW-Authenticate"), -1) {
			switch m[1] {
			case "resource_metadata":
				prmURL = m[2]
			case "scope":
				scope = m[2]
			}
		}
	}
	candidates := []string{}
	if prmURL != "" {
		candidates = append(candidates, prmURL)
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+p)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	issuer := ""
	for _, c := range candidates {
		var prm struct {
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		if getJSONDoc(c, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			issuer = prm.AuthorizationServers[0]
			if scope == "" && len(prm.ScopesSupported) > 0 {
				scope = strings.Join(prm.ScopesSupported, " ")
			}
			break
		}
	}
	if issuer == "" {
		issuer = origin
	}
	meta, err := authServerMetadata(issuer)
	if err != nil {
		return nil, "", err
	}
	return meta, scope, nil
}

// authServerMetadata reads RFC 8414 metadata, with the well-known segment
// inserted before the issuer's path as the RFC says, then OpenID's location.
func authServerMetadata(issuer string) (*authServerMeta, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")
	for _, c := range []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
		strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration",
	} {
		var m authServerMeta
		if getJSONDoc(c, &m) == nil && m.AuthorizationEndpoint != "" && m.TokenEndpoint != "" {
			return &m, nil
		}
	}
	return nil, fmt.Errorf("no OAuth authorization server metadata found for %s", issuer)
}

func getJSONDoc(u string, v any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := botOAuthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// registerOAuthClient performs dynamic client registration.
func registerOAuthClient(endpoint, redirect, botName string) (id, secret, method string, err error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "lasso bot " + botName,
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	res, err := botOAuthHTTP.Post(endpoint, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return "", "", "", fmt.Errorf("client registration refused (%s): %s", res.Status, previewText(string(raw)))
	}
	var out struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if json.Unmarshal(raw, &out) != nil || out.ClientID == "" {
		return "", "", "", fmt.Errorf("client registration returned no client_id")
	}
	method = out.TokenEndpointAuthMethod
	if method == "" {
		method = "none"
		if out.ClientSecret != "" {
			method = "client_secret_basic"
		}
	}
	return out.ClientID, out.ClientSecret, method, nil
}

// --- the flow -------------------------------------------------------------------

type botOAuthStart struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Redirect     string `json:"redirect_uri"`
	// Localhost: the browser will fail to load the redirect, and the human
	// pastes the address it landed on.
	Localhost bool `json:"localhost"`
}

// startBotOAuth begins a sign-in for one of the bot's http/sse servers.
// origin is the lasso origin the human's browser is on.
func startBotOAuth(rec *botRecord, server, origin string) (*botOAuthStart, error) {
	var srv *botMCPServer
	for i := range rec.MCP {
		if rec.MCP[i].Name == server {
			srv = &rec.MCP[i]
		}
	}
	if srv == nil || srv.Type == "stdio" {
		return nil, fmt.Errorf("%q is not one of the bot's http or sse servers", server)
	}
	ou, err := url.Parse(origin)
	if err != nil || (ou.Scheme != "https" && ou.Scheme != "http") || ou.Host == "" {
		return nil, fmt.Errorf("bad origin %q", origin)
	}
	meta, scope, err := discoverAuthServer(srv.URL)
	if err != nil {
		return nil, err
	}
	redirect := ou.Scheme + "://" + ou.Host + "/api/bots/oauth/callback"
	localhost := false
	clientID, secret, method := srv.OAuthClientID, "", "none"
	if clientID == "" {
		if meta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("%s does not support dynamic client registration; set a client id for this server", meta.Issuer)
		}
		clientID, secret, method, err = registerOAuthClient(meta.RegistrationEndpoint, redirect, rec.Name)
		if err != nil {
			// Some servers register native clients only. A localhost redirect
			// works for those: the human pastes where the browser lands.
			redirect = "http://localhost:53682/callback"
			localhost = true
			var err2 error
			clientID, secret, method, err2 = registerOAuthClient(meta.RegistrationEndpoint, redirect, rec.Name)
			if err2 != nil {
				return nil, err
			}
		}
	} else if srv.OAuthRedirect != "" {
		redirect, localhost = srv.OAuthRedirect, strings.Contains(srv.OAuthRedirect, "://localhost") || strings.Contains(srv.OAuthRedirect, "://127.0.0.1")
	}
	if srv.OAuthScope != "" {
		scope = srv.OAuthScope
	}
	verifier, err := randToken()
	if err != nil {
		return nil, err
	}
	state, err := randToken()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {srv.URL},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	botOAuthPend.Lock()
	for k, p := range botOAuthPend.m {
		if time.Since(p.created) > botOAuthPendingTTL {
			delete(botOAuthPend.m, k)
		}
	}
	botOAuthPend.m[state] = &botOAuthPending{
		bot: rec.Name, server: server, verifier: verifier, redirect: redirect,
		clientID: clientID, clientSecret: secret, authMethod: method,
		issuer: meta.Issuer, tokenEndpoint: meta.TokenEndpoint, resource: srv.URL, scope: scope,
		created: time.Now(),
	}
	botOAuthPend.Unlock()
	return &botOAuthStart{AuthorizeURL: authURL, State: state, Redirect: redirect, Localhost: localhost}, nil
}

// finishBotOAuth completes a sign-in from the redirect's query: the callback
// route's own, or the address a human pasted.
func finishBotOAuth(q url.Values) (*botOAuthPending, error) {
	state := q.Get("state")
	botOAuthPend.Lock()
	p := botOAuthPend.m[state]
	delete(botOAuthPend.m, state)
	botOAuthPend.Unlock()
	if p == nil || time.Since(p.created) > botOAuthPendingTTL {
		return nil, fmt.Errorf("this sign-in is unknown or has expired; press Sign in again")
	}
	if e := q.Get("error"); e != "" {
		return p, fmt.Errorf("the server refused: %s %s", e, q.Get("error_description"))
	}
	code := q.Get("code")
	if code == "" {
		return p, fmt.Errorf("the address has no authorization code in it")
	}
	tok, err := tokenRequest(p.tokenEndpoint, p.clientID, p.clientSecret, p.authMethod, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.redirect},
		"code_verifier": {p.verifier},
		"resource":      {p.resource},
	})
	if err != nil {
		return p, err
	}
	rec, err := getBot(p.bot)
	if err != nil {
		return p, err
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		return p, err
	}
	row := &botOAuthRow{
		Bot: p.bot, Server: p.server, Issuer: p.issuer, TokenEndpoint: p.tokenEndpoint,
		ClientID: p.clientID, AuthMethod: p.authMethod, Resource: p.resource, Scope: p.scope,
	}
	if err := storeBotTokens(b, rec, row, tok, p.clientSecret); err != nil {
		return p, err
	}
	return p, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// tokenRequest posts to the token endpoint, authenticating the client the way
// it was registered.
func tokenRequest(endpoint, clientID, secret, method string, form url.Values) (*tokenResponse, error) {
	switch method {
	case "client_secret_post":
		form.Set("client_id", clientID)
		form.Set("client_secret", secret)
	case "client_secret_basic":
	default:
		form.Set("client_id", clientID)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if method == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	res, err := botOAuthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error != "" {
			return nil, fmt.Errorf("token request refused: %s %s", e.Error, e.Desc)
		}
		return nil, fmt.Errorf("token request refused: %s", res.Status)
	}
	var t tokenResponse
	if err := json.Unmarshal(raw, &t); err != nil || t.AccessToken == "" {
		return nil, fmt.Errorf("the token response has no access_token")
	}
	if !botTokenSafe(t.AccessToken) {
		return nil, fmt.Errorf("the access token has characters a header cannot carry")
	}
	return &t, nil
}

// botTokenSafe: the helper prints the token inside a JSON string, so a quote
// or a backslash in it (no real token has one) would break the header.
func botTokenSafe(t string) bool {
	for _, c := range t {
		if c <= ' ' || c == '"' || c == '\\' || c > '~' {
			return false
		}
	}
	return true
}

// storeBotTokens writes a token response to the bot's fnox and records the
// rest. A response without a refresh token keeps the one already stored
// (servers that do not rotate it).
func storeBotTokens(b Backend, rec *botRecord, row *botOAuthRow, t *tokenResponse, clientSecret string) error {
	dir := expandTildeOn(b, rec.Dir)
	if err := botEnvSet(b, dir, botOAuthKey(row.Server, "ACCESS"), t.AccessToken, true); err != nil {
		return err
	}
	if t.RefreshToken != "" {
		if err := botEnvSet(b, dir, botOAuthKey(row.Server, "REFRESH"), t.RefreshToken, true); err != nil {
			return err
		}
	}
	if clientSecret != "" {
		if err := botEnvSet(b, dir, botOAuthKey(row.Server, "CLIENT_SECRET"), clientSecret, true); err != nil {
			return err
		}
	}
	row.ExpiresAt = 0
	if t.ExpiresIn > 0 {
		row.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).Unix()
	}
	if t.Scope != "" {
		row.Scope = t.Scope
	}
	row.Status, row.Error = "connected", ""
	if err := saveBotOAuth(row); err != nil {
		return err
	}
	// mcp.json now carries the headersHelper for this server.
	return botMaterialize(b, rec)
}

// refreshBotOAuth renews a server's access token. Called from the bot loop a
// few minutes ahead of expiry.
func refreshBotOAuth(b Backend, rec *botRecord, row *botOAuthRow) error {
	dir := expandTildeOn(b, rec.Dir)
	refresh, err := botRun(b, dir, "fnox", []string{"get", botOAuthKey(row.Server, "REFRESH")}, nil)
	if err != nil || strings.TrimSpace(string(refresh)) == "" {
		return fmt.Errorf("no refresh token stored; sign in again")
	}
	secret := ""
	if row.AuthMethod != "none" {
		if s, err := botRun(b, dir, "fnox", []string{"get", botOAuthKey(row.Server, "CLIENT_SECRET")}, nil); err == nil {
			secret = strings.TrimSpace(string(s))
		}
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {strings.TrimSpace(string(refresh))},
	}
	if row.Resource != "" {
		form.Set("resource", row.Resource)
	}
	t, err := tokenRequest(row.TokenEndpoint, row.ClientID, secret, row.AuthMethod, form)
	if err != nil {
		return err
	}
	return storeBotTokens(b, rec, row, t, "")
}

// botOAuthTick refreshes every token of the bot that expires within the next
// five minutes. A failure marks the server so the settings page says to sign
// in again; the bot keeps its old token until then.
func botOAuthTick(b Backend, rec *botRecord) {
	for _, row := range listBotOAuth(rec.Name) {
		if row.Status != "connected" || row.ExpiresAt == 0 || time.Until(time.Unix(row.ExpiresAt, 0)) > 5*time.Minute {
			continue
		}
		if err := refreshBotOAuth(b, rec, row); err != nil {
			log.Printf("bots:     %s: refresh %s: %v", rec.Name, row.Server, err)
			row.Status, row.Error = "error", err.Error()
			_ = saveBotOAuth(row)
		}
	}
}

// signOutBotOAuth forgets a server's tokens.
func signOutBotOAuth(b Backend, rec *botRecord, server string) error {
	dir := expandTildeOn(b, rec.Dir)
	for _, part := range []string{"ACCESS", "REFRESH", "CLIENT_SECRET"} {
		_ = botEnvUnset(b, dir, botOAuthKey(server, part))
	}
	deleteBotOAuth(rec.Name, server)
	return botMaterialize(b, rec)
}
