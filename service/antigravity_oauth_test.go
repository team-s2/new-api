package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestStartAndExchangeAntigravityOAuth(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			assert.Equal(t, "test-auth-code", r.Form.Get("code"))
			assert.Equal(t, AntigravityClientID, r.Form.Get("client_id"))
			assert.Equal(t, AntigravityClientSecret, r.Form.Get("client_secret"))
			assert.Equal(t, AntigravityRedirectURI, r.Form.Get("redirect_uri"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ag-access-123","refresh_token":"ag-refresh-456","expires_in":3600,"token_type":"Bearer"}`))
		case "refresh_token":
			assert.Equal(t, "ag-refresh-456", r.Form.Get("refresh_token"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ag-access-789","refresh_token":"ag-refresh-999","expires_in":3600,"token_type":"Bearer"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ag-access-123", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"dev@example.com"}`))
	})
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ag-access-123", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"cloudaicompanionProject": {"id": "ag-proj-001"},
			"currentTier": {"id": "free-tier", "name": "Free Tier"},
			"paidTier": {
				"id": "g1-pro-tier",
				"name": "Google One AI Pro",
				"availableCredits": [{"creditType": "GOOGLE_ONE_AI", "creditAmount": "850", "minimumCreditAmountForUsage": "5"}]
			}
		}`))
	})
	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-3-pro-high": {
					"displayName": "Gemini 3 Pro",
					"quotaInfo": {"remainingFraction": 0.75, "resetTime": "2026-10-19T00:00:00Z"}
				},
				"claude-sonnet-4-5": {
					"displayName": "Claude Sonnet 4.5",
					"quotaInfo": {"remainingFraction": 0.4, "resetTime": "2026-10-19T00:00:00Z"}
				}
			}
		}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	prevTokenEndpoint := antigravityTokenEndpointOverride
	prevUserInfoEndpoint := antigravityUserInfoEndpointOverride
	prevLoadCodeAssist := antigravityLoadCodeAssistOverride
	antigravityTokenEndpointOverride = srv.URL + "/token"
	antigravityUserInfoEndpointOverride = srv.URL + "/userinfo"
	antigravityLoadCodeAssistOverride = srv.URL + "/v1internal:loadCodeAssist"
	defer func() {
		antigravityTokenEndpointOverride = prevTokenEndpoint
		antigravityUserInfoEndpointOverride = prevUserInfoEndpoint
		antigravityLoadCodeAssistOverride = prevLoadCodeAssist
	}()

	const ownerSession = "admin-browser-session-ag-1"
	session, err := StartAntigravityOAuthLogin(ctx, ownerSession)
	require.NoError(t, err)
	require.NotEmpty(t, session.SessionID)
	require.NotEmpty(t, session.State)

	parsedAuthURL, err := url.Parse(session.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, AntigravityClientID, parsedAuthURL.Query().Get("client_id"))
	assert.Equal(t, AntigravityRedirectURI, parsedAuthURL.Query().Get("redirect_uri"))
	assert.Equal(t, "offline", parsedAuthURL.Query().Get("access_type"))
	assert.Equal(t, "consent", parsedAuthURL.Query().Get("prompt"))
	assert.Equal(t, session.State, parsedAuthURL.Query().Get("state"))

	callbackURL := "http://localhost:51121/oauth-callback?code=test-auth-code&state=" + url.QueryEscape(session.State)
	exchanged, err := ExchangeAntigravityOAuthCode(ctx, ownerSession, session.SessionID, callbackURL, "")
	require.NoError(t, err)
	assert.Equal(t, "ag-access-123", exchanged.Key.AccessToken)
	assert.Equal(t, "ag-refresh-456", exchanged.Key.RefreshToken)
	assert.Equal(t, "ag-proj-001", exchanged.Key.ProjectID)
	assert.Equal(t, "dev@example.com", exchanged.Key.Email)
	assert.Equal(t, "g1-pro-tier", exchanged.Key.PlanTier)

	// Session must be single-use (replay fails).
	_, err = ExchangeAntigravityOAuthCode(ctx, ownerSession, session.SessionID, callbackURL, "")
	assert.ErrorIs(t, err, ErrAntigravityOAuthSessionNotFound)

	// Usage inspection parses both loadCodeAssist and fetchAvailableModels.
	usage, err := FetchAntigravityUsage(ctx, srv.URL, exchanged.Credential, "")
	require.NoError(t, err)
	assert.Equal(t, "ag-proj-001", usage.ProjectID)
	assert.Equal(t, "dev@example.com", usage.Email)
	assert.Equal(t, "g1-pro-tier", usage.PaidTier)
	assert.Equal(t, "Google One AI Pro", usage.PaidTierName)
	require.Len(t, usage.Credits, 1)
	assert.Equal(t, "GOOGLE_ONE_AI", usage.Credits[0].CreditType)
	assert.Equal(t, "850", usage.Credits[0].CreditAmount)
	require.Len(t, usage.Models, 2)
	assert.Equal(t, "claude-sonnet-4-5", usage.Models[0].Model)
	require.NotNil(t, usage.Models[0].RemainingPercent)
	assert.InDelta(t, 40.0, *usage.Models[0].RemainingPercent, 0.01)
	assert.Equal(t, "gemini-3-pro-high", usage.Models[1].Model)
	require.NotNil(t, usage.Models[1].RemainingPercent)
	assert.InDelta(t, 75.0, *usage.Models[1].RemainingPercent, 0.01)

	// Model discovery maps upstream aliases and merges built-in models.
	models, err := FetchAntigravityChannelModels(ctx, srv.URL, exchanged.Credential, "")
	require.NoError(t, err)
	assert.Contains(t, models, "gemini-3-pro-preview")
	assert.Contains(t, models, "claude-sonnet-4-5")

	// Refresh token rotation.
	refreshed, err := RefreshAntigravityOAuthToken(ctx, exchanged.Key.RefreshToken, "")
	require.NoError(t, err)
	assert.Equal(t, "ag-access-789", refreshed.AccessToken)
	assert.Equal(t, "ag-refresh-999", refreshed.RefreshToken)
}

func TestAntigravityOAuthSecurityAndOnboardFallback(t *testing.T) {
	ctx := context.Background()
	const ownerA = "admin-session-a"
	const ownerB = "admin-session-b"

	_, err := StartAntigravityOAuthLogin(ctx, "")
	assert.ErrorIs(t, err, ErrAntigravityOAuthSessionOwnerMismatch)

	session, err := StartAntigravityOAuthLogin(ctx, ownerA)
	require.NoError(t, err)

	// Cross-admin session consumption must fail without consuming ownerA's session.
	_, err = ExchangeAntigravityOAuthCode(ctx, ownerB, session.SessionID, "http://localhost:51121/oauth-callback?code=abc&state="+session.State, "")
	assert.ErrorIs(t, err, ErrAntigravityOAuthSessionOwnerMismatch)

	// State mismatch must fail.
	_, err = ExchangeAntigravityOAuthCode(ctx, ownerA, session.SessionID, "http://localhost:51121/oauth-callback?code=abc&state=wrong-state", "")
	assert.ErrorIs(t, err, ErrAntigravityOAuthStateMismatch)

	// Invalid callback host must be rejected.
	_, err = parseAntigravityCallbackInput("https://evil.example.com/oauth-callback?code=abc&state=s1", "s1")
	assert.ErrorIs(t, err, ErrAntigravityOAuthInvalidInput)

	// Raw code without state when state is expected must be rejected.
	_, err = parseAntigravityCallbackInput("raw-code-only", "s1")
	assert.ErrorIs(t, err, ErrAntigravityOAuthStateMismatch)

	// Test onboardUser fallback when loadCodeAssist returns no project.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`))
	})
	mux.HandleFunc("/v1internal:onboardUser", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"done":true,"response":{"cloudaicompanionProject":{"id":"onboarded-proj-777"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	prevLoad := antigravityLoadCodeAssistOverride
	prevOnboard := antigravityOnboardUserOverride
	antigravityLoadCodeAssistOverride = srv.URL + "/v1internal:loadCodeAssist"
	antigravityOnboardUserOverride = srv.URL + "/v1internal:onboardUser"
	defer func() {
		antigravityLoadCodeAssistOverride = prevLoad
		antigravityOnboardUserOverride = prevOnboard
	}()

	projID, _, err := FetchAntigravityProjectAndTier(ctx, "test-token", "")
	require.NoError(t, err)
	assert.Equal(t, "onboarded-proj-777", projID)
}

func TestRefreshAntigravityChannelCredentialConcurrency(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:antigravity_refresh_test?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))

	origDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = origDB })

	oldKey := `{"access_token":"old-access","refresh_token":"old-refresh","project_id":"proj-1","email":"u@example.com","expired":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `"}`

	ch := &model.Channel{Type: constant.ChannelTypeAntigravitySub, Key: oldKey, Name: "ag-refresh-concurrency"}
	require.NoError(t, db.Create(ch).Error)
	model.CacheUpdateChannel(ch)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-refreshed-access","refresh_token":"new-refreshed-refresh","expires_in":3600}`))
	}))
	defer srv.Close()

	prevTokenEndpoint := antigravityTokenEndpointOverride
	antigravityTokenEndpointOverride = srv.URL
	defer func() { antigravityTokenEndpointOverride = prevTokenEndpoint }()

	rawKey, err := EnsureAntigravityChannelAccessToken(context.Background(), ch)
	require.NoError(t, err)
	ensuredKey, ok := ParseAntigravityOAuthKey(rawKey)
	require.True(t, ok)
	assert.Equal(t, "new-refreshed-access", ensuredKey.AccessToken)
	assert.Equal(t, oldKey, ch.Key, "lazy refresh must not mutate the caller's channel")

	gotKey, updatedCh, err := RefreshAntigravityChannelCredential(context.Background(), ch.Id)
	require.NoError(t, err)
	assert.Equal(t, "new-refreshed-access", gotKey.AccessToken)
	assert.Equal(t, "proj-1", gotKey.ProjectID)
	require.NotNil(t, updatedCh)

	var persisted model.Channel
	require.NoError(t, db.First(&persisted, ch.Id).Error)
	var persistedMap map[string]any
	require.NoError(t, common.Unmarshal([]byte(persisted.Key), &persistedMap))
	assert.Equal(t, "new-refreshed-access", persistedMap["access_token"])
	assert.Equal(t, "proj-1", persistedMap["project_id"])
}
