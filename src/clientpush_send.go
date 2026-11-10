package main

// The two push transports: APNs with a provider token (ES256 JWT from the .p8
// key, HTTP/2 to Apple) and FCM HTTP v1 (an OAuth access token from a service
// account's RS256 assertion). Stdlib only.

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The providers' addresses and the HTTP client; vars so tests stand in local
// servers (TLS, HTTP/2) for them.
var (
	apnsHosts = map[string]string{
		"production": "https://api.push.apple.com",
		"sandbox":    "https://api.sandbox.push.apple.com",
	}
	fcmSendBase    = "https://fcm.googleapis.com"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	pushHTTPClient = &http.Client{Timeout: 20 * time.Second}
)

// --- APNs ------------------------------------------------------------------------

// parseAPNsKey reads the .p8 key's PEM text, with real line breaks or with
// literal \n (as a single-line secret field holds it).
func parseAPNsKey(pemText string) (*ecdsa.PrivateKey, error) {
	pemText = strings.TrimSpace(pemText)
	if !strings.Contains(pemText, "\n") && strings.Contains(pemText, `\n`) {
		pemText = strings.ReplaceAll(pemText, `\n`, "\n")
	}
	blk, _ := pem.Decode([]byte(pemText))
	if blk == nil {
		return nil, errors.New("not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC key")
	}
	return ek, nil
}

type apnsJWT struct {
	token string
	at    time.Time
}

var apnsJWTs = struct {
	sync.Mutex
	m map[string]apnsJWT
}{m: map[string]apnsJWT{}}

const apnsJWTReuse = 50 * time.Minute

// apnsProviderToken is the provider JWT, reused for 50 minutes; fresh mints a
// new one.
func apnsProviderToken(c clientPushConfig, fresh bool, now time.Time) (string, error) {
	key := c.APNs.TeamID + "/" + c.APNs.KeyID
	apnsJWTs.Lock()
	cur, ok := apnsJWTs.m[key]
	apnsJWTs.Unlock()
	if ok && !fresh && now.Sub(cur.at) < apnsJWTReuse {
		return cur.token, nil
	}
	pemText, err := pushSecret("apns", c.APNs.KeyRef, 0)
	if err != nil {
		return "", err
	}
	k, err := parseAPNsKey(pemText)
	if err != nil {
		return "", err
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.APNs.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": c.APNs.TeamID, "iat": now.Unix()})
	signing := b64.EncodeToString(hdr) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	tok := signing + "." + b64.EncodeToString(sig)
	apnsJWTs.Lock()
	apnsJWTs.m[key] = apnsJWT{token: tok, at: now}
	apnsJWTs.Unlock()
	return tok, nil
}

// apnsPayload is a push as Apple's JSON.
func apnsPayload(n pushNote) []byte {
	if n.Kind == "badge" {
		b, _ := json.Marshal(map[string]any{"aps": map[string]any{"badge": n.Badge}, "lasso": map[string]any{"v": 1, "kind": "badge"}})
		return b
	}
	alert := map[string]any{"title": n.Title, "body": n.Body}
	if n.Subtitle != "" {
		alert["subtitle"] = n.Subtitle
	}
	aps := map[string]any{"alert": alert, "badge": n.Badge}
	if n.ThreadID != "" {
		aps["thread-id"] = n.ThreadID
	}
	if !n.Quiet {
		aps["sound"] = "default"
	}
	lasso := map[string]any{"v": 1, "kind": n.Kind}
	switch n.Kind {
	case "message":
		aps["category"] = "LASSO_MESSAGE"
		lasso["thread_id"], lasso["message_id"], lasso["bot"] = n.ThreadID, n.MessageID, n.Bot
	case "proposal":
		aps["category"] = "LASSO_PROPOSAL"
		lasso["thread_id"], lasso["proposal_id"], lasso["bot"] = n.ThreadID, n.ProposalID, n.Bot
	}
	b, _ := json.Marshal(map[string]any{"aps": aps, "lasso": lasso})
	return b
}

// sendAPNs sends to the token's environment; a BadDeviceToken there is tried
// once on the other environment (a build registered with the wrong one)
// before the token is given up, and a token that works there moves to it.
func sendAPNs(j pushJob) pushResult {
	env := j.tok.Environment
	if env != "sandbox" {
		env = "production"
	}
	res := sendAPNsTo(j, env)
	if res.Status == http.StatusBadRequest && res.Reason == "BadDeviceToken" {
		other := "sandbox"
		if env == "sandbox" {
			other = "production"
		}
		if alt := sendAPNsTo(j, other); alt.OK {
			_, _ = db.Exec(`UPDATE app_push_tokens SET environment = ? WHERE id = ?`, other, j.tok.ID)
			return alt
		} else if !alt.DeleteTok {
			// The other host failed for another reason: keep the token.
			res.DeleteTok = false
			res.Transient = alt.Transient
		}
	}
	return res
}

func sendAPNsTo(j pushJob, env string) pushResult {
	c := loadClientPushConfig()
	host, ok := apnsHosts[env]
	if !ok {
		host = apnsHosts["production"]
	}
	now := time.Now()
	for fresh := false; ; fresh = true {
		jwt, err := apnsProviderToken(c, fresh, now)
		if err != nil {
			return pushResult{Reason: "credentials_unreadable", Creds: true}
		}
		req, _ := http.NewRequest(http.MethodPost, host+"/3/device/"+url.PathEscape(j.tok.Token), bytes.NewReader(apnsPayload(j.note)))
		h := req.Header
		h.Set("authorization", "bearer "+jwt)
		h.Set("apns-topic", c.topic())
		h.Set("content-type", "application/json")
		if j.note.Kind == "badge" {
			h.Set("apns-push-type", "alert")
			h.Set("apns-priority", "5")
		} else {
			h.Set("apns-push-type", "alert")
			if j.note.Quiet {
				h.Set("apns-priority", "5")
			} else {
				h.Set("apns-priority", "10")
			}
			if j.note.ThreadID != "" {
				h.Set("apns-collapse-id", j.note.ThreadID)
			}
		}
		h.Set("apns-expiration", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
		resp, err := pushHTTPClient.Do(req)
		if err != nil {
			return pushResult{Reason: "network", Transient: true}
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body)
		resp.Body.Close()
		res := pushResult{Status: resp.StatusCode, Reason: body.Reason}
		switch {
		case resp.StatusCode == http.StatusOK:
			res.OK = true
		case resp.StatusCode == http.StatusGone, resp.StatusCode == http.StatusBadRequest && (body.Reason == "BadDeviceToken" || body.Reason == "DeviceTokenNotForTopic"):
			res.DeleteTok = true
		case resp.StatusCode == http.StatusForbidden && (body.Reason == "ExpiredProviderToken" || body.Reason == "InvalidProviderToken"):
			if !fresh {
				continue
			}
			res.Creds = true
		case resp.StatusCode == http.StatusForbidden:
			res.Creds = true
		case resp.StatusCode == http.StatusBadRequest && (body.Reason == "TopicDisallowed" || body.Reason == "BadTopic"):
			res.Creds = true
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			res.Transient = true
		}
		if res.Reason == "" && !res.OK {
			res.Reason = strconv.Itoa(resp.StatusCode)
		}
		return res
	}
}

// --- FCM ---------------------------------------------------------------------------

type fcmServiceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	key         *rsa.PrivateKey
}

func parseFCMCredentials(raw string) (*fcmServiceAccount, error) {
	var sa fcmServiceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return nil, err
	}
	if sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("a service account needs project_id, client_email and private_key")
	}
	blk, _ := pem.Decode([]byte(sa.PrivateKey))
	if blk == nil {
		return nil, errors.New("private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		if rk, err2 := x509.ParsePKCS1PrivateKey(blk.Bytes); err2 == nil {
			k = rk
		} else {
			return nil, err
		}
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private_key is not RSA")
	}
	sa.key = rk
	return &sa, nil
}

type fcmAccessToken struct {
	token   string
	expires time.Time
}

var fcmTokens = struct {
	sync.Mutex
	m map[string]fcmAccessToken
}{m: map[string]fcmAccessToken{}}

// fcmAccess is an OAuth access token for the service account, cached until
// five minutes before it expires.
func fcmAccess(sa *fcmServiceAccount, fresh bool, now time.Time) (string, error) {
	fcmTokens.Lock()
	cur, ok := fcmTokens.m[sa.ClientEmail]
	fcmTokens.Unlock()
	if ok && !fresh && now.Before(cur.expires.Add(-5*time.Minute)) {
		return cur.token, nil
	}
	hdr := `{"alg":"RS256","typ":"JWT"}`
	claims, _ := json.Marshal(map[string]any{"iss": sa.ClientEmail, "scope": "https://www.googleapis.com/auth/firebase.messaging",
		"aud": "https://oauth2.googleapis.com/token", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	signing := b64.EncodeToString([]byte(hdr)) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, sa.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + b64.EncodeToString(sig)}}
	resp, err := pushHTTPClient.PostForm(googleTokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint answered %d", resp.StatusCode)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	fcmTokens.Lock()
	fcmTokens.m[sa.ClientEmail] = fcmAccessToken{token: out.AccessToken, expires: now.Add(time.Duration(out.ExpiresIn) * time.Second)}
	fcmTokens.Unlock()
	return out.AccessToken, nil
}

// fcmMessage is a push as FCM HTTP v1's data-only message.
func fcmMessage(token string, n pushNote) []byte {
	data := map[string]string{"v": "1", "kind": n.Kind, "badge": strconv.Itoa(n.Badge)}
	android := map[string]any{"ttl": "86400s"}
	if n.Kind == "badge" {
		android["priority"] = "normal"
	} else {
		android["priority"] = "high"
		data["title"], data["body"] = n.Title, n.Body
		if n.Subtitle != "" {
			data["subtitle"] = n.Subtitle
		}
		data["quiet"] = "0"
		if n.Quiet {
			data["quiet"] = "1"
		}
		if n.ThreadID != "" {
			data["thread_id"] = n.ThreadID
			data["url"] = "lasso://thread/" + n.ThreadID
			android["collapse_key"] = n.ThreadID
		}
		if n.Bot != "" {
			data["bot"] = n.Bot
		}
		switch n.Kind {
		case "message":
			data["message_id"] = n.MessageID
		case "proposal":
			data["proposal_id"] = n.ProposalID
		}
	}
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"token": token, "data": data, "android": android}})
	return b
}

func sendFCM(j pushJob) pushResult {
	c := loadClientPushConfig()
	raw, err := pushSecret("fcm", c.FCM.CredentialsRef, 10*time.Minute)
	if err != nil {
		return pushResult{Reason: "credentials_unreadable", Creds: true}
	}
	sa, err := parseFCMCredentials(raw)
	if err != nil {
		return pushResult{Reason: "credentials_unreadable", Creds: true}
	}
	now := time.Now()
	for fresh := false; ; fresh = true {
		access, err := fcmAccess(sa, fresh, now)
		if err != nil {
			return pushResult{Reason: "token_exchange", Creds: true}
		}
		req, _ := http.NewRequest(http.MethodPost, fcmSendBase+"/v1/projects/"+url.PathEscape(sa.ProjectID)+"/messages:send", bytes.NewReader(fcmMessage(j.tok.Token, j.note)))
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Content-Type", "application/json")
		resp, err := pushHTTPClient.Do(req)
		if err != nil {
			return pushResult{Reason: "network", Transient: true}
		}
		var body struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
				Details []struct {
					ErrorCode       string `json:"errorCode"`
					FieldViolations []struct {
						Field string `json:"field"`
					} `json:"fieldViolations"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&body)
		resp.Body.Close()
		code := body.Error.Status
		namesToken := strings.Contains(strings.ToLower(body.Error.Message), "token")
		for _, d := range body.Error.Details {
			if d.ErrorCode != "" {
				code = d.ErrorCode
			}
			for _, f := range d.FieldViolations {
				if strings.Contains(f.Field, "token") {
					namesToken = true
				}
			}
		}
		res := pushResult{Status: resp.StatusCode, Reason: code}
		switch {
		case resp.StatusCode == http.StatusOK:
			res.OK = true
		case resp.StatusCode == http.StatusNotFound && code == "UNREGISTERED",
			resp.StatusCode == http.StatusBadRequest && code == "INVALID_ARGUMENT" && namesToken,
			resp.StatusCode == http.StatusForbidden && code == "SENDER_ID_MISMATCH":
			res.DeleteTok = true
		case resp.StatusCode == http.StatusUnauthorized && !fresh:
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			res.Creds = true
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			res.Transient = true
		}
		if res.Reason == "" && !res.OK {
			res.Reason = strconv.Itoa(resp.StatusCode)
		}
		return res
	}
}
