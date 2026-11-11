package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Devin subscription OAuth, reverse engineered from the official devin-cli
// (strings in the installed binary confirm the endpoints): PKCE
// authorization-code flow against app.devin.ai/auth/cli/continue whose
// redirect_uri is the literal marker `chisel-show-auth-token` — the web page
// then displays the authorization code for manual paste, which suits a
// server-side gateway that cannot host a loopback listener.
const (
	devinOAuthAuthorizeURL = "https://app.devin.ai/auth/cli/continue"
	devinOAuthTokenURL     = "https://api.devin.ai/auth/cli/token"
	devinOAuthSelfURL      = "https://api.devin.ai/v3/self"
	devinOAuthRedirectURI  = "chisel-show-auth-token"
	devinOAuthSessionTTL   = 10 * time.Minute
	devinOAuthHTTPTimeout  = 30 * time.Second
)

// devinOAuthTokenEndpoint is a var so tests can point it at a local server.
var devinOAuthTokenEndpoint = devinOAuthTokenURL

// DevinOAuthSession is one pending PKCE flow.
type DevinOAuthSession = pkceOAuthSession

var devinOAuthSessionStore = pkceOAuthSessionStore{
	redisKeyPrefix: "new-api:devin-oauth:",
	ttl:            devinOAuthSessionTTL,
}

func devinOAuthRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type DevinOAuthAuthURL struct {
	AuthorizeURL string
	SessionID    string
}

// StartDevinOAuthLogin builds the PKCE authorization URL. The caller opens it
// in a browser, signs in, and pastes the displayed code back into
// ExchangeDevinOAuthCode.
func StartDevinOAuthLogin(ownerSession string) (*DevinOAuthAuthURL, error) {
	if ownerSession == "" {
		return nil, common.NewMessage("Browser session is required. Please log in again")
	}

	state, err := devinOAuthRandomHex(32)
	if err != nil {
		return nil, fmt.Errorf("devin oauth: generate state: %w", err)
	}
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("devin oauth: generate verifier: %w", err)
	}
	codeVerifier := strings.TrimRight(base64.URLEncoding.EncodeToString(verifierBytes), "=")
	challengeSum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := strings.TrimRight(base64.URLEncoding.EncodeToString(challengeSum[:]), "=")
	sessionID, err := devinOAuthRandomHex(16)
	if err != nil {
		return nil, fmt.Errorf("devin oauth: generate session id: %w", err)
	}

	if err := devinOAuthSessionStore.store(sessionID, &DevinOAuthSession{
		OwnerSession:  ownerSession,
		State:         state,
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
		CreatedAt:     time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("devin oauth: store session: %w", err)
	}

	params := url.Values{}
	params.Set("redirect_uri", devinOAuthRedirectURI)
	params.Set("state", state)
	params.Set("prompt", "select_account")
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("cli_pkce_marker", "1")

	return &DevinOAuthAuthURL{
		AuthorizeURL: devinOAuthAuthorizeURL + "?" + params.Encode(),
		SessionID:    sessionID,
	}, nil
}

// parseDevinOAuthCallback accepts a pasted URL (with code/state params), a
// query string, or a bare authorization code.
func parseDevinOAuthCallback(input string, session *DevinOAuthSession) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", common.NewMessage("Authorization code is required")
	}
	if strings.Contains(trimmed, "=") {
		code := ""
		state := ""
		if parsed, err := url.Parse(trimmed); err == nil && parsed != nil {
			values := parsed.Query()
			code = strings.TrimSpace(values.Get("code"))
			state = strings.TrimSpace(values.Get("state"))
		}
		if code == "" {
			if values, err := url.ParseQuery(strings.TrimPrefix(trimmed, "?")); err == nil {
				code = strings.TrimSpace(values.Get("code"))
				state = strings.TrimSpace(values.Get("state"))
			}
		}
		if code != "" {
			if state == "" || !constantTimeEqual(state, session.State) {
				return "", common.NewMessage("OAuth state mismatch. Please restart OAuth login")
			}
			return code, nil
		}
	}
	if strings.ContainsAny(trimmed, " \t\r\n/?&") {
		return "", common.NewMessage("Authorization callback does not contain a valid code")
	}
	return trimmed, nil
}

func devinOAuthHTTPClient(proxyURL string) (*http.Client, error) {
	base, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = &http.Client{}
	}
	clientCopy := *base
	clientCopy.Timeout = devinOAuthHTTPTimeout
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &clientCopy, nil
}

type devinOAuthTokenResponse struct {
	Token        string `json:"token"`
	SessionToken string `json:"session_token"`
	AccessToken  string `json:"access_token"`
}

// ExchangeDevinOAuthCode swaps the pasted authorization code for a Devin
// session token and assembles the channel credential JSON.
func ExchangeDevinOAuthCode(ctx context.Context, sessionID, input, proxyURL, ownerSession string) (string, error) {
	session, ok := devinOAuthSessionStore.consume(sessionID, ownerSession)
	if !ok {
		return "", common.NewMessage("OAuth session was not found or has expired. Please restart OAuth login")
	}

	code, err := parseDevinOAuthCallback(input, session)
	if err != nil {
		return "", err
	}

	client, err := devinOAuthHTTPClient(proxyURL)
	if err != nil {
		return "", err
	}

	payload, err := common.Marshal(map[string]string{
		"code":          code,
		"code_verifier": session.CodeVerifier,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, devinOAuthTokenEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("devin oauth: token endpoint returned status %d", resp.StatusCode)
	}

	var tokenResp devinOAuthTokenResponse
	if err := common.Unmarshal(body, &tokenResp); err != nil {
		return "", errors.New("devin oauth: invalid token response")
	}
	rawToken := strings.TrimSpace(tokenResp.Token)
	if rawToken == "" {
		rawToken = strings.TrimSpace(tokenResp.SessionToken)
	}
	if rawToken == "" {
		rawToken = strings.TrimSpace(tokenResp.AccessToken)
	}
	if rawToken == "" {
		return "", errors.New("devin oauth: token response missing token")
	}
	sessionToken := NormalizeDevinSessionToken(rawToken)

	cred := DevinCredential{
		SessionToken: sessionToken,
		CreatedAt:    time.Now().Format(time.RFC3339),
	}
	// Best-effort profile enrichment; failures keep the credential usable.
	if profile, err := fetchDevinSelf(ctx, client, sessionToken); err == nil {
		cred.UserName = profile.UserName
		cred.UserID = profile.UserID
		cred.OrgID = profile.OrgID
	}

	encoded, err := common.Marshal(cred)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type devinSelfProfile struct {
	UserName string `json:"user_name"`
	UserID   string `json:"user_id"`
	OrgID    string `json:"org_id"`
}

func fetchDevinSelf(ctx context.Context, client *http.Client, token string) (*devinSelfProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, devinOAuthSelfURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devin oauth: /v3/self returned status %d", resp.StatusCode)
	}
	var profile devinSelfProfile
	if err := common.Unmarshal(body, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}
