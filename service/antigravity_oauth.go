package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	DefaultAntigravityBaseURL   = "https://daily-cloudcode-pa.googleapis.com"
	AntigravityProdBaseURL      = "https://cloudcode-pa.googleapis.com"
	AntigravitySandboxBaseURL   = "https://autopush-cloudcode-pa.sandbox.googleapis.com"
	AntigravityAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	AntigravityTokenEndpoint    = "https://oauth2.googleapis.com/token"
	AntigravityUserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
	AntigravityClientID         = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep" + ".apps.googleusercontent.com"
	AntigravityClientSecret     = "GOCSPX-" + "K58FWR486LdLJ1mLB8sXC4z6qDAf"
	AntigravityRedirectURI      = "http://localhost:51121/oauth-callback"
	AntigravityScopes           = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs"
	DefaultAntigravityUserAgent = "antigravity/hub/2.23.0 darwin/arm64"
	antigravityAPIClient        = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityClientMetadata   = `{"ideType":"ANTIGRAVITY","platform":"MACOS","pluginType":"GEMINI"}`

	antigravityOAuthSessionTTL       = 10 * time.Minute
	antigravityOAuthRedisKeyPrefix   = "new-api:antigravity-oauth:"
	antigravityOAuthHTTPTimeout      = 25 * time.Second
	antigravityOAuthMaxResponseBytes = 2 << 20
)

var (
	ErrAntigravityOAuthSessionNotFound      = errors.New("antigravity oauth session not found or expired")
	ErrAntigravityOAuthStateMismatch        = errors.New("antigravity oauth state mismatch")
	ErrAntigravityOAuthSessionOwnerMismatch = errors.New("antigravity oauth session owner mismatch")
	ErrAntigravityOAuthCodeEmpty            = errors.New("authorization code is required")
	ErrAntigravityOAuthInvalidInput         = errors.New("invalid callback URL or authorization code")
	ErrAntigravityOAuthUpstreamFailed       = errors.New("antigravity oauth token exchange failed")
)

var (
	antigravityAuthEndpointOverride     string
	antigravityTokenEndpointOverride    string
	antigravityUserInfoEndpointOverride string
	antigravityLoadCodeAssistOverride   string
	antigravityOnboardUserOverride      string
)

// AntigravityOAuthKey is the JSON credential stored in model.Channel.Key for
// ChannelTypeAntigravitySub.
type AntigravityOAuthKey struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProjectID    string `json:"project_id,omitempty"`
	Email        string `json:"email,omitempty"`
	PlanTier     string `json:"plan_tier,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
	Expired      string `json:"expired,omitempty"`
}

type AntigravityOAuthLoginSession struct {
	SessionID    string `json:"session_id"`
	State        string `json:"state"`
	AuthorizeURL string `json:"authorize_url"`
	RedirectURI  string `json:"redirect_uri"`
	ExpiresIn    int    `json:"expires_in"`
}

type AntigravityOAuthExchangeResult struct {
	Credential string              `json:"credential"`
	Key        AntigravityOAuthKey `json:"key"`
}

type antigravityOAuthStoredSession struct {
	State        string `json:"state"`
	OwnerSession string `json:"owner_session"`
	CreatedAt    int64  `json:"created_at"`
}

type antigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

var antigravityOAuthMemorySessions sync.Map

// antigravityIDEMetadata identifies the client to the Cloud Code Assist setup
// endpoints (loadCodeAssist / onboardUser).
var antigravityIDEMetadata = map[string]string{
	"ideType":    "ANTIGRAVITY",
	"platform":   "PLATFORM_UNSPECIFIED",
	"pluginType": "GEMINI",
}

func ResolveAntigravityUserAgent() string {
	if envUA := strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_AGENT")); envUA != "" {
		return envUA
	}
	return DefaultAntigravityUserAgent
}

func ApplyAntigravityHeaders(headers http.Header, accessToken string, isStream bool) {
	if headers == nil {
		return
	}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", ResolveAntigravityUserAgent())
	headers.Set("X-Goog-Api-Client", antigravityAPIClient)
	headers.Set("Client-Metadata", antigravityClientMetadata)
	if isStream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
}

func ParseAntigravityOAuthKey(rawKey string) (*AntigravityOAuthKey, bool) {
	trimmed := strings.TrimSpace(rawKey)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var key AntigravityOAuthKey
	if err := common.Unmarshal([]byte(trimmed), &key); err != nil {
		return nil, false
	}
	key.AccessToken = strings.TrimSpace(key.AccessToken)
	key.RefreshToken = strings.TrimSpace(key.RefreshToken)
	key.ProjectID = strings.TrimSpace(key.ProjectID)
	key.Email = strings.TrimSpace(key.Email)
	key.PlanTier = strings.TrimSpace(key.PlanTier)
	if key.AccessToken == "" {
		return nil, false
	}
	return &key, true
}

func ExtractAntigravityAccessToken(rawKey string) (string, error) {
	trimmed := strings.TrimSpace(rawKey)
	if trimmed == "" {
		return "", errors.New("empty antigravity credential")
	}
	if strings.HasPrefix(trimmed, "{") {
		key, ok := ParseAntigravityOAuthKey(trimmed)
		if !ok {
			return "", errors.New("antigravity key JSON must include access_token")
		}
		return key.AccessToken, nil
	}
	return trimmed, nil
}

func StartAntigravityOAuthLogin(ctx context.Context, ownerSession string) (*AntigravityOAuthLoginSession, error) {
	ownerSession = strings.TrimSpace(ownerSession)
	if ownerSession == "" {
		return nil, ErrAntigravityOAuthSessionOwnerMismatch
	}
	state, err := grokOAuthRandomHex(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}
	sessionID, err := grokOAuthRandomHex(24)
	if err != nil {
		return nil, fmt.Errorf("failed to generate session id: %w", err)
	}

	stored := antigravityOAuthStoredSession{
		State:        state,
		OwnerSession: ownerSession,
		CreatedAt:    time.Now().Unix(),
	}
	if err := saveAntigravityOAuthSession(ctx, sessionID, stored); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("client_id", AntigravityClientID)
	params.Set("redirect_uri", AntigravityRedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", AntigravityScopes)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("state", state)

	authEndpoint := AntigravityAuthEndpoint
	if antigravityAuthEndpointOverride != "" {
		authEndpoint = antigravityAuthEndpointOverride
	}

	return &AntigravityOAuthLoginSession{
		SessionID:    sessionID,
		State:        state,
		AuthorizeURL: authEndpoint + "?" + params.Encode(),
		RedirectURI:  AntigravityRedirectURI,
		ExpiresIn:    int(antigravityOAuthSessionTTL.Seconds()),
	}, nil
}

func ExchangeAntigravityOAuthCode(ctx context.Context, ownerSession, sessionID, callbackInput, proxyURL string) (*AntigravityOAuthExchangeResult, error) {
	ownerSession = strings.TrimSpace(ownerSession)
	sessionID = strings.TrimSpace(sessionID)
	if ownerSession == "" {
		return nil, ErrAntigravityOAuthSessionOwnerMismatch
	}
	if sessionID == "" {
		return nil, ErrAntigravityOAuthSessionNotFound
	}

	stored, err := consumeAntigravityOAuthSession(ctx, sessionID, ownerSession)
	if err != nil {
		return nil, err
	}

	code, err := parseAntigravityCallbackInput(callbackInput, stored.State)
	if err != nil {
		return nil, err
	}

	tokenResp, err := requestAntigravityTokenExchange(ctx, code, proxyURL)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	key := AntigravityOAuthKey{
		AccessToken:  strings.TrimSpace(tokenResp.AccessToken),
		RefreshToken: strings.TrimSpace(tokenResp.RefreshToken),
		LastRefresh:  now.Format(time.RFC3339),
		Expired:      now.Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339),
	}

	if email, emailErr := fetchAntigravityUserEmail(ctx, key.AccessToken, proxyURL); emailErr == nil && email != "" {
		key.Email = email
	}
	if projectID, planTier, projErr := FetchAntigravityProjectAndTier(ctx, key.AccessToken, proxyURL); projErr == nil {
		key.ProjectID = projectID
		key.PlanTier = planTier
	}

	raw, err := common.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal antigravity credential: %w", err)
	}
	return &AntigravityOAuthExchangeResult{
		Credential: string(raw),
		Key:        key,
	}, nil
}

func RefreshAntigravityOAuthToken(ctx context.Context, refreshToken, proxyURL string) (*AntigravityOAuthKey, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("refresh_token is empty")
	}

	form := url.Values{}
	form.Set("client_id", AntigravityClientID)
	form.Set("client_secret", AntigravityClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	tokenResp, err := postAntigravityTokenForm(ctx, form, proxyURL)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	nextRefresh := strings.TrimSpace(tokenResp.RefreshToken)
	if nextRefresh == "" {
		nextRefresh = refreshToken
	}

	return &AntigravityOAuthKey{
		AccessToken:  strings.TrimSpace(tokenResp.AccessToken),
		RefreshToken: nextRefresh,
		LastRefresh:  now.Format(time.RFC3339),
		Expired:      now.Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

func FetchAntigravityProjectAndTier(ctx context.Context, accessToken, proxyURL string) (string, string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", "", errors.New("access_token is empty")
	}

	client, err := newAntigravityHTTPClient(proxyURL, antigravityOAuthHTTPTimeout)
	if err != nil {
		return "", "", err
	}

	status, bodyBytes, err := postAntigravityCloudCode(ctx, client, antigravityLoadCodeAssistEndpoint(), accessToken, map[string]any{
		"metadata": antigravityIDEMetadata,
	})
	if err != nil {
		return "", "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("loadCodeAssist failed with status %d", status)
	}

	var result map[string]any
	if err := common.Unmarshal(bodyBytes, &result); err != nil {
		return "", "", err
	}

	projectID := extractAntigravityProjectID(result)
	planTier := extractAntigravityPlanTier(result)
	if projectID != "" {
		return projectID, planTier, nil
	}

	defaultTierID := extractAntigravityDefaultTierID(result)
	if defaultTierID == "" {
		defaultTierID = "legacy-tier"
	}
	onboardedProjectID, err := onboardAntigravityUser(ctx, client, accessToken, defaultTierID)
	if err != nil {
		return "", planTier, err
	}
	return onboardedProjectID, planTier, nil
}

func antigravityLoadCodeAssistEndpoint() string {
	if antigravityLoadCodeAssistOverride != "" {
		return antigravityLoadCodeAssistOverride
	}
	return AntigravityProdBaseURL + "/v1internal:loadCodeAssist"
}

// postAntigravityCloudCode sends a JSON POST to a Cloud Code Assist
// v1internal endpoint with the Antigravity client headers and returns the
// upstream status with a size-limited body.
func postAntigravityCloudCode(ctx context.Context, client *http.Client, endpoint, accessToken string, payload any) (int, []byte, error) {
	rawBody, err := common.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return 0, nil, err
	}
	ApplyAntigravityHeaders(req.Header, accessToken, false)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func extractAntigravityProjectID(result map[string]any) string {
	for _, field := range []string{"cloudaicompanionProject", "projectId", "project"} {
		raw, ok := result[field]
		if !ok || raw == nil {
			continue
		}
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if m, ok := raw.(map[string]any); ok {
			if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

func extractAntigravityPlanTier(result map[string]any) string {
	if paidTier, ok := result["paidTier"].(map[string]any); ok {
		if id, ok := paidTier["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	if currentTier, ok := result["currentTier"].(map[string]any); ok {
		if id, ok := currentTier["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func extractAntigravityDefaultTierID(result map[string]any) string {
	allowedTiers, ok := result["allowedTiers"].([]any)
	if !ok {
		return ""
	}
	for _, item := range allowedTiers {
		tierMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		isDefault, _ := tierMap["isDefault"].(bool)
		if !isDefault {
			continue
		}
		if id, ok := tierMap["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func onboardAntigravityUser(ctx context.Context, client *http.Client, accessToken, tierID string) (string, error) {
	onboardURL := DefaultAntigravityBaseURL + "/v1internal:onboardUser"
	if antigravityOnboardUserOverride != "" {
		onboardURL = antigravityOnboardUserOverride
	}

	metadata := map[string]string{
		"ideVersion": "2.23.0",
		"ideName":    "antigravity",
	}
	maps.Copy(metadata, antigravityIDEMetadata)
	payload := map[string]any{
		"tierId":   tierID,
		"metadata": metadata,
	}

	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		status, bodyBytes, err := postAntigravityCloudCode(ctx, client, onboardURL, accessToken, payload)
		if err != nil {
			return "", err
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			return "", fmt.Errorf("onboardUser failed with status %d", status)
		}

		var result map[string]any
		if err := common.Unmarshal(bodyBytes, &result); err != nil {
			return "", err
		}
		done, _ := result["done"].(bool)
		if done {
			if responseMap, ok := result["response"].(map[string]any); ok {
				if projectID := extractAntigravityProjectID(responseMap); projectID != "" {
					return projectID, nil
				}
			}
			if projectID := extractAntigravityProjectID(result); projectID != "" {
				return projectID, nil
			}
			return "", nil
		}
	}
	return "", errors.New("onboardUser timed out")
}

func fetchAntigravityUserEmail(ctx context.Context, accessToken, proxyURL string) (string, error) {
	endpoint := AntigravityUserInfoEndpoint
	if antigravityUserInfoEndpointOverride != "" {
		endpoint = antigravityUserInfoEndpointOverride
	}
	client, err := newAntigravityHTTPClient(proxyURL, antigravityOAuthHTTPTimeout)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
	if err != nil {
		return "", err
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := common.Unmarshal(body, &info); err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Email), nil
}

func requestAntigravityTokenExchange(ctx context.Context, code, proxyURL string) (*antigravityTokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", AntigravityClientID)
	form.Set("client_secret", AntigravityClientSecret)
	form.Set("redirect_uri", AntigravityRedirectURI)
	form.Set("grant_type", "authorization_code")

	return postAntigravityTokenForm(ctx, form, proxyURL)
}

func postAntigravityTokenForm(ctx context.Context, form url.Values, proxyURL string) (*antigravityTokenResponse, error) {
	endpoint := AntigravityTokenEndpoint
	if antigravityTokenEndpointOverride != "" {
		endpoint = antigravityTokenEndpointOverride
	}

	client, err := newAntigravityHTTPClient(proxyURL, antigravityOAuthHTTPTimeout)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ResolveAntigravityUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAntigravityOAuthUpstreamFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read response", ErrAntigravityOAuthUpstreamFailed)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: upstream status %d", ErrAntigravityOAuthUpstreamFailed, resp.StatusCode)
	}

	var parsed antigravityTokenResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: invalid response payload", ErrAntigravityOAuthUpstreamFailed)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return nil, fmt.Errorf("%w: missing access_token in response", ErrAntigravityOAuthUpstreamFailed)
	}
	return &parsed, nil
}

func newAntigravityHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	base, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = &http.Client{}
	}
	clientCopy := *base
	clientCopy.Timeout = timeout
	return &clientCopy, nil
}

func parseAntigravityCallbackInput(rawInput, expectedState string) (string, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return "", ErrAntigravityOAuthCodeEmpty
	}

	var query url.Values
	isCallbackURL := strings.Contains(trimmed, "://")
	if isCallbackURL || strings.HasPrefix(trimmed, "?") || strings.Contains(trimmed, "code=") || strings.Contains(trimmed, "error=") {
		if isCallbackURL {
			parsedURL, err := url.Parse(trimmed)
			if err != nil {
				return "", ErrAntigravityOAuthInvalidInput
			}
			host := strings.ToLower(parsedURL.Hostname())
			if (host != "localhost" && host != "127.0.0.1") ||
				(parsedURL.Path != "" && parsedURL.Path != "/oauth-callback") {
				return "", ErrAntigravityOAuthInvalidInput
			}
			query = parsedURL.Query()
		} else {
			q := strings.TrimPrefix(trimmed, "?")
			parsedQuery, err := url.ParseQuery(q)
			if err != nil {
				return "", ErrAntigravityOAuthInvalidInput
			}
			query = parsedQuery
		}

		if errCode := strings.TrimSpace(query.Get("error")); errCode != "" {
			desc := strings.TrimSpace(query.Get("error_description"))
			if desc != "" {
				return "", fmt.Errorf("%w: %s (%s)", ErrAntigravityOAuthInvalidInput, errCode, desc)
			}
			return "", fmt.Errorf("%w: %s", ErrAntigravityOAuthInvalidInput, errCode)
		}

		gotState := strings.TrimSpace(query.Get("state"))
		if expectedState != "" {
			if gotState == "" || subtle.ConstantTimeCompare([]byte(gotState), []byte(expectedState)) != 1 {
				return "", ErrAntigravityOAuthStateMismatch
			}
		}

		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			return "", ErrAntigravityOAuthCodeEmpty
		}
		return code, nil
	}

	if expectedState != "" {
		return "", ErrAntigravityOAuthStateMismatch
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return "", ErrAntigravityOAuthInvalidInput
	}
	return trimmed, nil
}

func saveAntigravityOAuthSession(ctx context.Context, sessionID string, session antigravityOAuthStoredSession) error {
	if common.RedisEnabled && common.RDB != nil {
		raw, err := common.Marshal(session)
		if err != nil {
			return err
		}
		if err := common.RDB.Set(ctx, antigravityOAuthRedisKeyPrefix+sessionID, string(raw), antigravityOAuthSessionTTL).Err(); err == nil {
			return nil
		}
	}
	pruneExpiredAntigravityOAuthMemorySessions()
	antigravityOAuthMemorySessions.Store(sessionID, session)
	return nil
}

const consumeAntigravityOAuthSessionLua = `
local raw = redis.call("GET", KEYS[1])
if not raw then
	return {0, ""}
end
local ok, data = pcall(cjson.decode, raw)
if not ok or type(data) ~= "table" then
	redis.call("DEL", KEYS[1])
	return {-2, ""}
end
if data["owner_session"] ~= ARGV[1] then
	return {-1, ""}
end
redis.call("DEL", KEYS[1])
return {1, raw}
`

func consumeAntigravityOAuthSession(ctx context.Context, sessionID, ownerSession string) (*antigravityOAuthStoredSession, error) {
	if common.RedisEnabled && common.RDB != nil {
		res, err := common.RDB.Eval(ctx, consumeAntigravityOAuthSessionLua, []string{antigravityOAuthRedisKeyPrefix + sessionID}, ownerSession).Slice()
		if err == nil && len(res) == 2 {
			code, _ := res[0].(int64)
			raw, _ := res[1].(string)
			switch code {
			case 1:
				var stored antigravityOAuthStoredSession
				if err := common.Unmarshal([]byte(raw), &stored); err != nil {
					return nil, ErrAntigravityOAuthSessionNotFound
				}
				return &stored, nil
			case -1:
				return nil, ErrAntigravityOAuthSessionOwnerMismatch
			case -2:
				return nil, ErrAntigravityOAuthSessionNotFound
			}
		}
	}

	val, ok := antigravityOAuthMemorySessions.Load(sessionID)
	if !ok {
		return nil, ErrAntigravityOAuthSessionNotFound
	}
	stored, ok := val.(antigravityOAuthStoredSession)
	if !ok {
		antigravityOAuthMemorySessions.Delete(sessionID)
		return nil, ErrAntigravityOAuthSessionNotFound
	}
	if time.Since(time.Unix(stored.CreatedAt, 0)) > antigravityOAuthSessionTTL {
		antigravityOAuthMemorySessions.Delete(sessionID)
		return nil, ErrAntigravityOAuthSessionNotFound
	}
	if stored.OwnerSession == "" || subtle.ConstantTimeCompare([]byte(stored.OwnerSession), []byte(ownerSession)) != 1 {
		return nil, ErrAntigravityOAuthSessionOwnerMismatch
	}
	if _, loaded := antigravityOAuthMemorySessions.LoadAndDelete(sessionID); !loaded {
		return nil, ErrAntigravityOAuthSessionNotFound
	}
	return &stored, nil
}

func pruneExpiredAntigravityOAuthMemorySessions() {
	now := time.Now()
	antigravityOAuthMemorySessions.Range(func(k, v any) bool {
		if stored, ok := v.(antigravityOAuthStoredSession); ok {
			if now.Sub(time.Unix(stored.CreatedAt, 0)) > antigravityOAuthSessionTTL {
				antigravityOAuthMemorySessions.Delete(k)
			}
		}
		return true
	})
}
