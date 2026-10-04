package service

import (
	"encoding/base64"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func base64URLSegment(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestParseGrokOAuthCallback(t *testing.T) {
	session := &GrokOAuthSession{State: "st-123"}

	code, err := parseGrokOAuthCallback("http://127.0.0.1:56121/callback?code=abc&state=st-123", session)
	require.NoError(t, err)
	assert.Equal(t, "abc", code)

	code, err = parseGrokOAuthCallback("code=abc&state=st-123", session)
	require.NoError(t, err)
	assert.Equal(t, "abc", code)

	code, err = parseGrokOAuthCallback("abc", session)
	require.NoError(t, err)
	assert.Equal(t, "abc", code, "bare code accepted without state")

	_, err = parseGrokOAuthCallback("http://127.0.0.1:56121/callback?code=abc&state=wrong", session)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state mismatch")

	_, err = parseGrokOAuthCallback("http://127.0.0.1:56121/callback?code=abc", session)
	require.Error(t, err, "callback with code but no state must be rejected")

	_, err = parseGrokOAuthCallback("", session)
	require.Error(t, err)

	_, err = parseGrokOAuthCallback("not a code with spaces", session)
	require.Error(t, err)
}

func TestParseGrokOAuthCallbackExpiredSession(t *testing.T) {
	session := &GrokOAuthSession{State: "st", CreatedAt: time.Now().Add(-2 * grokOAuthSessionTTL)}
	_, err := parseGrokOAuthCallback("code=x&state=st", session)
	// parse itself is time-independent; expiry is enforced by ExchangeGrokOAuthCode
	require.NoError(t, err)
}

func TestGrokOAuthAuthorizeURLShape(t *testing.T) {
	result, err := StartGrokOAuthLogin()
	require.NoError(t, err)
	require.NotEmpty(t, result.SessionID)

	parsed, err := url.Parse(result.AuthorizeURL)
	require.NoError(t, err)
	assert.Equal(t, "auth.x.ai", parsed.Host)
	assert.Equal(t, "/oauth2/authorize", parsed.Path)

	query := parsed.Query()
	assert.Equal(t, "code", query.Get("response_type"))
	assert.Equal(t, grokOAuthClientID, query.Get("client_id"))
	assert.Equal(t, grokOAuthRedirectURI, query.Get("redirect_uri"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.NotEmpty(t, query.Get("state"))
	assert.NotEmpty(t, query.Get("code_challenge"))
	assert.NotEmpty(t, query.Get("nonce"))
}

func TestBuildGrokOAuthKeyClaims(t *testing.T) {
	// header.payload.sig with a minimal payload carrying tier/email
	payload := base64URLSegment(`{"email":"u@example.com","sub":"user-1","tier":5}`)
	credential, err := buildGrokOAuthKey(&grokOAuthTokenResponse{
		AccessToken:  "jwt." + payload + ".sig",
		RefreshToken: "rt",
		ExpiresIn:    3600,
	})
	require.NoError(t, err)

	key, err := ParseGrokOAuthKey(credential)
	require.NoError(t, err)
	assert.Equal(t, "jwt."+payload+".sig", key.AccessToken)
	assert.Equal(t, "rt", key.RefreshToken)
	assert.Equal(t, "u@example.com", key.Email)
	assert.Equal(t, "user-1", key.Subject)
	assert.Equal(t, "supergrok_heavy", key.PlanTier)
	assert.NotEmpty(t, key.Expired)
}

func TestGrokTierName(t *testing.T) {
	assert.Equal(t, "free", grokTierName(0))
	assert.Equal(t, "supergrok", grokTierName(1))
	assert.Equal(t, "supergrok_heavy", grokTierName(5))
	assert.Equal(t, "9", grokTierName(9))
}
