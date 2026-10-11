package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cloudflare Access JWT verification.
//
// Behind a tunnel, Access forwards every request it admitted with a signed
// assertion in Cf-Access-Jwt-Assertion. Checking it here, rather than trusting
// that the request came through Access, is what makes a request that reached
// the port some other way (a second tunnel, a local process, a misconfigured
// hostname with no Access app) fail. Cloudflare's own guidance is to validate
// the header, not the cookie, which "is not guaranteed to be passed":
// https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/
//
// RS256 only. Keys come from https://<team>.cloudflareaccess.com/cdn-cgi/access/certs,
// cached for an hour, refetched when an unknown kid appears, and refetched at
// most once a minute, so a flood of made-up key ids cannot become a flood of
// requests to Cloudflare. Keys rotate every six weeks and the old one stays
// valid for seven days, so the cache never strands a live token.

const (
	accessTeamEnv = "LASSO_ACCESS_TEAM"
	accessAUDEnv  = "LASSO_ACCESS_AUD"
	accessJWTHdr  = "Cf-Access-Jwt-Assertion"

	accessKeysTTL      = time.Hour
	accessRefetchEvery = time.Minute
	accessLeeway       = time.Minute
)

type accessVerifier struct {
	issuer   string
	certsURL string
	auds     map[string]bool

	fetch func(url string) ([]byte, error)
	now   func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

// accessIdentity is what a verified assertion says: a person (Email) or a
// service token (CommonName, its client id).
type accessIdentity struct {
	Email      string
	Subject    string
	CommonName string
}

// normalizeAccessTeam accepts "team", "team.cloudflareaccess.com" or the full
// https URL and returns the team domain.
func normalizeAccessTeam(team string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(team))
	t = strings.TrimPrefix(t, "https://")
	t = strings.TrimSuffix(t, "/")
	if t == "" {
		return "", errors.New("empty team name")
	}
	if !strings.Contains(t, ".") {
		t += ".cloudflareaccess.com"
	}
	if strings.ContainsAny(t, "/?#@ ") {
		return "", fmt.Errorf("%q is not a team domain", team)
	}
	return t, nil
}

func newAccessVerifier(team string, auds []string) (*accessVerifier, error) {
	domain, err := normalizeAccessTeam(team)
	if err != nil {
		return nil, err
	}
	v := &accessVerifier{
		issuer:   "https://" + domain,
		certsURL: "https://" + domain + "/cdn-cgi/access/certs",
		auds:     map[string]bool{},
		fetch:    fetchHTTPS,
		now:      time.Now,
	}
	for _, a := range auds {
		if a = strings.TrimSpace(a); a != "" {
			v.auds[a] = true
		}
	}
	if len(v.auds) == 0 {
		return nil, errors.New("no application AUD tag")
	}
	return v, nil
}

// accessVerifierFromEnv builds the verifier from LASSO_ACCESS_TEAM and
// LASSO_ACCESS_AUD; nil when neither is set.
func accessVerifierFromEnv() (*accessVerifier, error) {
	team := envList(accessTeamEnv)
	auds := envList(accessAUDEnv)
	if len(team) == 0 && len(auds) == 0 {
		return nil, nil
	}
	if len(team) != 1 {
		return nil, fmt.Errorf("%s must name one Access team", accessTeamEnv)
	}
	if len(auds) == 0 {
		return nil, fmt.Errorf("%s is required with %s (the application's Audience tag)", accessAUDEnv, accessTeamEnv)
	}
	return newAccessVerifier(team[0], auds)
}

func fetchHTTPS(url string) ([]byte, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (v *accessVerifier) verifyRequest(r *http.Request) (accessIdentity, error) {
	tok := strings.TrimSpace(r.Header.Get(accessJWTHdr))
	if tok == "" {
		return accessIdentity{}, errors.New("no " + accessJWTHdr)
	}
	return v.verify(tok)
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// audience is a JWT aud: a string or a list of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

type accessClaims struct {
	Aud        audience `json:"aud"`
	Iss        string   `json:"iss"`
	Exp        float64  `json:"exp"`
	Nbf        float64  `json:"nbf"`
	Email      string   `json:"email"`
	Sub        string   `json:"sub"`
	CommonName string   `json:"common_name"`
}

func (v *accessVerifier) verify(token string) (accessIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return accessIdentity{}, errors.New("malformed assertion")
	}
	var h jwtHeader
	if err := decodeSegment(parts[0], &h); err != nil {
		return accessIdentity{}, errors.New("malformed assertion header")
	}
	if h.Alg != "RS256" {
		return accessIdentity{}, fmt.Errorf("assertion algorithm %q is not RS256", h.Alg)
	}
	key, err := v.key(h.Kid)
	if err != nil {
		return accessIdentity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return accessIdentity{}, errors.New("malformed assertion signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return accessIdentity{}, errors.New("assertion signature does not verify")
	}
	var c accessClaims
	if err := decodeSegment(parts[1], &c); err != nil {
		return accessIdentity{}, errors.New("malformed assertion claims")
	}
	if c.Iss != v.issuer {
		return accessIdentity{}, fmt.Errorf("assertion issued by %q, not %q", c.Iss, v.issuer)
	}
	audOK := false
	for _, a := range c.Aud {
		if v.auds[a] {
			audOK = true
			break
		}
	}
	if !audOK {
		return accessIdentity{}, errors.New("assertion is for another Access application")
	}
	now := v.now()
	if c.Exp == 0 || now.After(time.Unix(int64(c.Exp), 0).Add(accessLeeway)) {
		return accessIdentity{}, errors.New("assertion expired")
	}
	if c.Nbf != 0 && now.Add(accessLeeway).Before(time.Unix(int64(c.Nbf), 0)) {
		return accessIdentity{}, errors.New("assertion not yet valid")
	}
	id := accessIdentity{Email: c.Email, Subject: c.Sub, CommonName: c.CommonName}
	if id.Email == "" && id.CommonName == "" {
		return accessIdentity{}, errors.New("assertion names neither a user nor a service token")
	}
	return id, nil
}

func decodeSegment(seg string, into any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// key returns the public key for kid, fetching the team's keys when they are
// stale or the kid is unknown, never more than once a minute.
func (v *accessVerifier) key(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	k, known := v.keys[kid]
	stale := now.Sub(v.fetchedAt) > accessKeysTTL
	if (known && !stale) || now.Sub(v.triedAt) < accessRefetchEvery {
		if known {
			return k, nil
		}
		return nil, errors.New("assertion signed by an unknown key")
	}
	v.triedAt = now
	body, err := v.fetch(v.certsURL)
	if err == nil {
		var keys map[string]*rsa.PublicKey
		if keys, err = parseJWKS(body); err == nil {
			v.keys, v.fetchedAt = keys, now
		}
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetching Access keys: %w", err)
	}
	return nil, errors.New("assertion signed by an unknown key")
}

func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, err
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("no RSA keys in the Access certs document")
	}
	return out, nil
}
