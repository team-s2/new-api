package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Grok subscription OAuth against auth.x.ai, reverse engineered from the
// official Grok CLI: PKCE authorization-code flow whose redirect URI is the
// CLI's local loopback (http://127.0.0.1:56121/callback). The browser cannot
// reach that callback while this gateway runs elsewhere, so the admin copies
// the failed-navigation URL from the address bar and pastes it back; the code
// is exchanged server-side. Refresh is a standard refresh_token grant.
const (
	grokOAuthIssuer       = "https://auth.x.ai"
	grokOAuthAuthorizeURL = grokOAuthIssuer + "/oauth2/authorize"
	grokOAuthTokenURL     = grokOAuthIssuer + "/oauth2/token"
	grokOAuthClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	grokOAuthScope        = "openid profile email offline_access grok-cli:access api:access"
	grokOAuthRedirectURI  = "http://127.0.0.1:56121/callback"
	grokOAuthSessionTTL   = 10 * time.Minute
	grokOAuthHTTPTimeout  = 30 * time.Second
)

// grokOAuthTokenEndpoint is a var so tests can point it at a local server.
var grokOAuthTokenEndpoint = grokOAuthTokenURL

// GrokOAuthSession is one pending PKCE flow.
type GrokOAuthSession struct {
	State         string
	CodeVerifier  string
	CodeChallenge string
	CreatedAt     time.Time
}

var (
	grokOAuthSessionsMu sync.Mutex
	grokOAuthSessions   = make(map[string]*GrokOAuthSession)
)

func grokOAuthCleanupLoop() {
	for {
		time.Sleep(time.Minute)
		grokOAuthSessionsMu.Lock()
		for id, session := range grokOAuthSessions {
			if time.Since(session.CreatedAt) > grokOAuthSessionTTL {
				delete(grokOAuthSessions, id)
			}
		}
		grokOAuthSessionsMu.Unlock()
	}
}

var grokOAuthCleanupOnce sync.Once

func grokOAuthRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type GrokOAuthAuthURL struct {
	AuthorizeURL string
	SessionID    string
}

// StartGrokOAuthLogin builds the auth.x.ai PKCE authorization URL. The caller
// opens it in a browser, signs in, and pastes the resulting (failed-loopback)
// callback URL back into ExchangeGrokOAuthCode.
func StartGrokOAuthLogin() (*GrokOAuthAuthURL, error) {
	grokOAuthCleanupOnce.Do(func() { go grokOAuthCleanupLoop() })

	state, err := grokOAuthRandomHex(32)
	if err != nil {
		return nil, fmt.Errorf("grok oauth: generate state: %w", err)
	}
	nonce, err := grokOAuthRandomHex(16)
	if err != nil {
		return nil, fmt.Errorf("grok oauth: generate nonce: %w", err)
	}
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("grok oauth: generate verifier: %w", err)
	}
	codeVerifier := strings.TrimRight(base64.URLEncoding.EncodeToString(verifierBytes), "=")
	challengeSum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := strings.TrimRight(base64.URLEncoding.EncodeToString(challengeSum[:]), "=")
	sessionID, err := grokOAuthRandomHex(16)
	if err != nil {
		return nil, fmt.Errorf("grok oauth: generate session id: %w", err)
	}

	grokOAuthSessionsMu.Lock()
	grokOAuthSessions[sessionID] = &GrokOAuthSession{
		State:         state,
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
		CreatedAt:     time.Now(),
	}
	grokOAuthSessionsMu.Unlock()

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", grokOAuthClientID)
	params.Set("redirect_uri", grokOAuthRedirectURI)
	params.Set("scope", grokOAuthScope)
	params.Set("state", state)
	params.Set("nonce", nonce)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("plan", "generic")

	return &GrokOAuthAuthURL{
		AuthorizeURL: grokOAuthAuthorizeURL + "?" + params.Encode(),
		SessionID:    sessionID,
	}, nil
}

// parseGrokOAuthCallback accepts the full pasted loopback callback URL, a bare
// query string, or a bare authorization code, validating state when present.
func parseGrokOAuthCallback(input string, session *GrokOAuthSession) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("grok oauth: empty callback input")
	}
	code := ""
	state := ""
	if strings.Contains(trimmed, "=") {
		if parsed, err := url.Parse(trimmed); err == nil && parsed != nil {
			values := parsed.Query()
			code = strings.TrimSpace(values.Get("code"))
			state = strings.TrimSpace(values.Get("state"))
		}
		if code == "" {
			query := strings.TrimPrefix(trimmed, "?")
			if values, err := url.ParseQuery(query); err == nil {
				code = strings.TrimSpace(values.Get("code"))
				state = strings.TrimSpace(values.Get("state"))
			}
		}
		if code != "" {
			if state == "" || !constantTimeEqual(state, session.State) {
				return "", errors.New("grok oauth: state mismatch, please restart OAuth login")
			}
			return code, nil
		}
	}
	if strings.Contains(trimmed, " ") || strings.Contains(trimmed, "/") {
		return "", errors.New("grok oauth: callback input does not contain an authorization code")
	}
	return trimmed, nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

type grokOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

func grokOAuthHTTPClient(proxyURL string) (*http.Client, error) {
	base, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	if base == nil {
		return &http.Client{Timeout: grokOAuthHTTPTimeout}, nil
	}
	clientCopy := *base
	clientCopy.Timeout = grokOAuthHTTPTimeout
	return &clientCopy, nil
}

func grokOAuthPostForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "newapi-grok-oauth/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("grok oauth: endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := common.Unmarshal(body, out); err != nil {
		return fmt.Errorf("grok oauth: decode token response: %w", err)
	}
	return nil
}

// ExchangeGrokOAuthCode swaps the pasted callback (or bare code) for a
// complete Grok subscription channel credential JSON.
func ExchangeGrokOAuthCode(ctx context.Context, sessionID, input, proxyURL string) (string, error) {
	grokOAuthSessionsMu.Lock()
	session, ok := grokOAuthSessions[sessionID]
	if ok {
		delete(grokOAuthSessions, sessionID)
	}
	grokOAuthSessionsMu.Unlock()
	if !ok || time.Since(session.CreatedAt) > grokOAuthSessionTTL {
		return "", errors.New("grok oauth: session not found or expired, please restart OAuth login")
	}

	code, err := parseGrokOAuthCallback(input, session)
	if err != nil {
		return "", err
	}

	client, err := grokOAuthHTTPClient(proxyURL)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", grokOAuthClientID)
	form.Set("code", code)
	form.Set("redirect_uri", grokOAuthRedirectURI)
	form.Set("code_verifier", session.CodeVerifier)

	var tokenResp grokOAuthTokenResponse
	if err := grokOAuthPostForm(ctx, client, grokOAuthTokenEndpoint, form, &tokenResp); err != nil {
		return "", err
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", errors.New("grok oauth: token response missing access_token")
	}
	return buildGrokOAuthKey(&tokenResp)
}

// RefreshGrokOAuthToken refreshes an access token with the refresh_token
// grant. xAI may omit refresh_token on rotation-less responses; keep the old
// one in that case.
func RefreshGrokOAuthToken(ctx context.Context, refreshToken, proxyURL string) (*grokOAuthTokenResponse, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, errors.New("grok oauth: empty refresh_token")
	}
	client, err := grokOAuthHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", grokOAuthClientID)
	form.Set("refresh_token", rt)

	var tokenResp grokOAuthTokenResponse
	if err := grokOAuthPostForm(ctx, client, grokOAuthTokenEndpoint, form, &tokenResp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, errors.New("grok oauth: refresh response missing access_token")
	}
	if strings.TrimSpace(tokenResp.RefreshToken) == "" {
		tokenResp.RefreshToken = rt
	}
	return &tokenResp, nil
}

// GrokOAuthKey mirrors the JSON credential stored in the Grok Subscription
// channel key (relay/channel/groksub has its own copy for the relay path).
type GrokOAuthKey struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`

	Email       string `json:"email,omitempty"`
	Subject     string `json:"sub,omitempty"`
	PlanTier    string `json:"plan_tier,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	Expired     string `json:"expired,omitempty"`
}

func ParseGrokOAuthKey(raw string) (*GrokOAuthKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("grok subscription channel: empty oauth key")
	}
	var key GrokOAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("grok subscription channel: invalid oauth key json")
	}
	return &key, nil
}

// buildGrokOAuthKey enriches the token with identity claims from the access
// token JWT and encodes the channel key JSON.
func buildGrokOAuthKey(tokenResp *grokOAuthTokenResponse) (string, error) {
	key := GrokOAuthKey{
		AccessToken:  strings.TrimSpace(tokenResp.AccessToken),
		RefreshToken: strings.TrimSpace(tokenResp.RefreshToken),
		IDToken:      strings.TrimSpace(tokenResp.IDToken),
		LastRefresh:  time.Now().Format(time.RFC3339),
	}
	if tokenResp.ExpiresIn > 0 {
		key.Expired = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	if claims := decodeGrokJWTClaims(key.AccessToken); claims != nil {
		key.Email, _ = claims["email"].(string)
		key.Subject, _ = claims["sub"].(string)
		key.PlanTier = grokPlanTierFromClaims(claims)
	}
	encoded, err := common.Marshal(key)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// grokPlanTierFromClaims maps the numeric JWT `tier` claim onto stable keys.
func grokPlanTierFromClaims(claims map[string]any) string {
	raw, ok := claims["tier"]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case float64:
		return grokTierName(uint64(v))
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return grokTierName(uint64(n))
		}
	case string:
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
			return grokTierName(n)
		}
		return strings.TrimSpace(v)
	}
	return ""
}

func grokTierName(tier uint64) string {
	switch tier {
	case 0:
		return "free"
	case 1:
		return "supergrok"
	case 2:
		return "x_basic"
	case 3:
		return "x_premium"
	case 4:
		return "x_premium_plus"
	case 5:
		return "supergrok_heavy"
	case 6:
		return "supergrok_lite"
	case 7:
		return "supergrok_plus"
	default:
		return strconv.FormatUint(tier, 10)
	}
}

func decodeGrokJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := common.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}
