package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestGrokOAuthAuthorizeURLShape(t *testing.T) {
	result, err := StartGrokOAuthLogin("browser-session")
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

func TestGrokOAuthSessionUsesRedisAcrossWorkers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldRedisEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = client
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.RDB = oldRDB
		grokOAuthSessionsMu.Lock()
		grokOAuthSessions = make(map[string]*GrokOAuthSession)
		grokOAuthSessionsMu.Unlock()
		_ = client.Close()
	})

	flow, err := StartGrokOAuthLogin("browser-session")
	require.NoError(t, err)

	// Simulate the completion request arriving at another worker.
	grokOAuthSessionsMu.Lock()
	delete(grokOAuthSessions, flow.SessionID)
	grokOAuthSessionsMu.Unlock()

	session, ok := consumeGrokOAuthSession(flow.SessionID, "browser-session")
	require.True(t, ok)
	require.NotNil(t, session)
	assert.Equal(t, "browser-session", session.OwnerSession)

	_, ok = consumeGrokOAuthSession(flow.SessionID, "browser-session")
	assert.False(t, ok, "OAuth sessions must be single-use")
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

func TestGrokCredentialRefreshDatabaseMatrix(t *testing.T) {
	InitHttpClient()
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			dbType := common.DatabaseTypeSQLite
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "grok.db") + "?_pragma=busy_timeout(10000)")
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
				dbType = common.DatabaseTypeMySQL
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
				dbType = common.DatabaseTypePostgreSQL
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			oldDB, oldCache, oldType, oldEndpoint := model.DB, common.MemoryCacheEnabled, common.MainDatabaseType(), grokOAuthTokenEndpoint
			model.DB = db
			common.MemoryCacheEnabled = true
			common.SetMainDatabaseType(dbType)
			t.Cleanup(func() {
				model.DB = oldDB
				common.MemoryCacheEnabled = oldCache
				common.SetMainDatabaseType(oldType)
				grokOAuthTokenEndpoint = oldEndpoint
				_ = sqlDB.Close()
			})
			require.NoError(t, db.AutoMigrate(&model.Channel{}))
			var version string
			query := "select version()"
			if dialect == "sqlite" {
				query = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s %s", dialect, version)
			var calls atomic.Int32
			var fail atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if fail.Load() {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
					return
				}
				if !assert.NoError(t, r.ParseForm()) {
					return
				}
				assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
			}))
			defer server.Close()
			grokOAuthTokenEndpoint = server.URL
			oldKey := fmt.Sprintf(`{"access_token":"old-access","refresh_token":"old-refresh","expired":%q}`, time.Now().Add(-time.Hour).Format(time.RFC3339))
			ch := &model.Channel{Type: constant.ChannelTypeGrokSub, Key: oldKey, Name: "grok-refresh-review"}
			require.NoError(t, db.Create(ch).Error)
			t.Cleanup(func() { db.Delete(&model.Channel{}, ch.Id) })
			model.CacheUpdateChannel(ch)
			var wg sync.WaitGroup
			results := make(chan string, 2)
			failures := make(chan error, 2)
			for range 2 {
				wg.Go(func() {
					key, err := EnsureGrokChannelAccessToken(context.Background(), ch)
					results <- key
					failures <- err
				})
			}
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				require.NoError(t, err)
			}
			for raw := range results {
				key, err := ParseGrokOAuthKey(raw)
				require.NoError(t, err)
				assert.Equal(t, "new-access", key.AccessToken)
			}
			assert.Equal(t, int32(1), calls.Load(), "concurrent callers must reuse the refreshed credential")
			cached, err := model.CacheGetChannel(ch.Id)
			require.NoError(t, err)
			saved, err := model.GetChannelById(ch.Id, true)
			require.NoError(t, err)
			assert.Equal(t, saved.Key, cached.Key)
			assert.NotEqual(t, oldKey, cached.Key)
			// A caller may already hold the stale cache snapshot when another request refreshes.
			_, err = EnsureGrokChannelAccessToken(context.Background(), ch)
			require.NoError(t, err)
			assert.Equal(t, int32(1), calls.Load())
			refreshed, returned, err := RefreshGrokChannelCredential(context.Background(), ch.Id)
			require.NoError(t, err)
			assert.Equal(t, int32(2), calls.Load())
			returnedKey, err := ParseGrokOAuthKey(returned.Key)
			require.NoError(t, err)
			assert.Equal(t, refreshed, returnedKey)
			fail.Store(true)
			_, _, err = RefreshGrokChannelCredential(context.Background(), ch.Id)
			require.Error(t, err)
			afterFailure, err := model.GetChannelById(ch.Id, true)
			require.NoError(t, err)
			assert.Equal(t, returned.Key, afterFailure.Key, "failed refresh must preserve the credential")
		})
	}
}

func TestGrokOAuthExchangeSessionSecurity(t *testing.T) {
	oldRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedisEnabled })
	InitHttpClient()
	oldEndpoint := grokOAuthTokenEndpoint
	t.Cleanup(func() { grokOAuthTokenEndpoint = oldEndpoint })
	var calls atomic.Int32
	var challenge string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !assert.NoError(t, r.ParseForm()) {
			return
		}
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		assert.Equal(t, challenge, base64.RawURLEncoding.EncodeToString(digest[:]))
		assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		assert.Equal(t, grokOAuthRedirectURI, r.Form.Get("redirect_uri"))
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`)
	}))
	defer server.Close()
	grokOAuthTokenEndpoint = server.URL
	_, err := StartGrokOAuthLogin("")
	require.Error(t, err)
	for _, tc := range []struct {
		name       string
		expired    bool
		wrongState bool
	}{{"valid", false, false}, {"expired", true, false}, {"wrong-state", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			flow, err := StartGrokOAuthLogin("browser-session")
			require.NoError(t, err)
			authURL, err := url.Parse(flow.AuthorizeURL)
			require.NoError(t, err)
			challenge = authURL.Query().Get("code_challenge")
			state := authURL.Query().Get("state")
			if tc.wrongState {
				state = "wrong"
			}
			callback := "http://127.0.0.1:56121/callback?code=code&state=" + state
			if tc.expired {
				grokOAuthSessionsMu.Lock()
				grokOAuthSessions[flow.SessionID].CreatedAt = time.Now().Add(-2 * grokOAuthSessionTTL)
				grokOAuthSessionsMu.Unlock()
			}
			before := calls.Load()
			_, err = ExchangeGrokOAuthCode(context.Background(), flow.SessionID, callback, "", "another-session")
			require.Error(t, err)
			assert.Equal(t, before, calls.Load())
			_, err = ExchangeGrokOAuthCode(context.Background(), flow.SessionID, callback, "", "browser-session")
			if tc.expired || tc.wrongState {
				require.Error(t, err)
				assert.Equal(t, before, calls.Load())
			} else {
				require.NoError(t, err)
				assert.Equal(t, before+1, calls.Load())
			}
			_, err = ExchangeGrokOAuthCode(context.Background(), flow.SessionID, callback, "", "browser-session")
			require.Error(t, err)
		})
	}
}

func TestGrokOAuthRefreshFailuresDoNotExposeCredentials(t *testing.T) {
	InitHttpClient()
	oldEndpoint := grokOAuthTokenEndpoint
	t.Cleanup(func() { grokOAuthTokenEndpoint = oldEndpoint })
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	for _, status := range []int{http.StatusBadRequest, http.StatusTemporaryRedirect, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = io.WriteString(w, `{"expires_in":"secret-refresh"}`)
					return
				}
				_, _ = io.WriteString(w, `{"refresh_token":"secret-refresh"}`)
			}))
			defer server.Close()
			grokOAuthTokenEndpoint = server.URL
			_, err := RefreshGrokOAuthToken(context.Background(), "secret-refresh", "")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-refresh")
			assert.False(t, redirected.Load())
		})
	}
}
