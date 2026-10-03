package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGitHubLoginGrantDatabaseMatrix(t *testing.T) {
	useTestSessionSecret(t)
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "github.db"))
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			oldDB, oldRedis, oldOptions, oldType := model.DB, common.RedisEnabled, common.OptionMap, common.MainDatabaseType()
			model.DB, common.RedisEnabled = db, false
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			if dialect == "postgres" {
				common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
			}
			t.Cleanup(func() {
				model.DB = oldDB
				common.RedisEnabled = oldRedis
				common.OptionMap = oldOptions
				common.SetMainDatabaseType(oldType)
				sqlDB.Close()
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.Option{}))
			var version string
			query := "select version()"
			if dialect == "sqlite" {
				query = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s: %s", dialect, version)
			for _, tc := range []struct {
				name                   string
				initialRole, grantRole int
				mfa                    bool
				failure                string
			}{
				{"promote-admin", 1, 10, false, ""}, {"promote-root", 1, 100, false, ""},
				{"keep-role", 1, 0, false, ""}, {"preserve-root", 100, 10, false, ""},
				{"mfa-root", 1, 100, true, ""}, {"policy-changed", 1, 100, true, "policy"},
				{"binding-changed", 1, 100, true, "binding"}, {"user-disabled", 1, 100, false, "disabled"},
				{"mfa-expired", 1, 100, true, "expired"},
				{"old-mfa-challenge", 1, 100, true, "missing-grant"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw := fmt.Sprintf(`{"enabled":true,"user_ids":["42"],"role":%d}`, tc.grantRole)
					common.OptionMap = map[string]string{common.GitHubAccessPolicyKey: raw}
					require.NoError(t, model.UpdateOption(common.GitHubAccessPolicyKey, raw))
					var saved model.Option
					require.NoError(t, db.Where(&model.Option{Key: common.GitHubAccessPolicyKey}).First(&saved).Error)
					require.Equal(t, raw, saved.Value)
					require.Error(t, model.UpdateOption(common.GitHubAccessPolicyKey, `{"enabled":false,"role":100}`))
					user := &model.User{Username: fmt.Sprintf("gh-%d", time.Now().UnixNano()), Role: tc.initialRole, Status: common.UserStatusEnabled, AuthVersion: 1, GitHubId: "42", AffCode: tc.name}
					require.NoError(t, db.Create(user).Error)
					t.Cleanup(func() { db.Unscoped().Delete(&model.User{}, user.Id) })
					oldBundle, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
					require.NoError(t, err)
					oldIdentity, err := ParseAccessToken(oldBundle.AccessToken)
					require.NoError(t, err)
					grant := &common.GitHubLoginGrant{UserID: "42", Policy: raw}
					var bundle *AuthBundle
					if tc.mfa {
						require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, IsEnabled: true, Secret: "unused"}).Error)
						var challenge *LoginChallenge
						if tc.failure == "missing-grant" {
							challenge, err = StartLoginVerification(user, "oauth:github", nil)
						} else {
							challenge, err = StartLoginVerification(user, "oauth:github", nil, grant)
						}
						require.NoError(t, err)
						require.NotNil(t, challenge)
						var pending model.User
						require.NoError(t, db.First(&pending, user.Id).Error)
						require.Equal(t, tc.initialRole, pending.Role, "MFA challenge must not grant privileges")
						var verification *LoginVerification
						verification, err = RequireLoginVerification(challenge.FlowToken, VerificationMethodTwoFA)
						require.NoError(t, err)
						switch tc.failure {
						case "policy":
							common.OptionMap[common.GitHubAccessPolicyKey] = `{"enabled":true}`
						case "binding":
							require.NoError(t, db.Model(user).Update("github_id", "43").Error)
						case "expired":
							require.NoError(t, db.Model(&model.AuthFlow{}).Where("id = ?", verification.Flow.Id).Update("expires_at", time.Now().Add(-time.Minute)).Error)
						}
						bundle, _, err = CompleteLoginVerification(challenge.FlowToken, verification, VerificationMethodTwoFA, "127.0.0.1", "test")
						if tc.failure == "" {
							require.NoError(t, err)
							_, _, replayErr := CompleteLoginVerification(challenge.FlowToken, verification, VerificationMethodTwoFA, "127.0.0.1", "test")
							require.Error(t, replayErr)
						}
					} else {
						if tc.failure == "disabled" {
							require.NoError(t, db.Model(user).Update("status", common.UserStatusDisabled).Error)
						}
						bundle, err = CreateLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "oauth:github", "127.0.0.1", "test", grant)
					}
					var current model.User
					require.NoError(t, db.First(&current, user.Id).Error)
					if tc.failure != "" {
						require.Error(t, err)
						require.Nil(t, bundle)
						require.Equal(t, tc.initialRole, current.Role)
						return
					}
					require.NoError(t, err)
					require.NotNil(t, bundle)
					require.Equal(t, max(tc.initialRole, tc.grantRole), current.Role)
					identity, err := ParseAccessToken(bundle.AccessToken)
					require.NoError(t, err)
					_, cached, err := ValidateLoginSession(identity)
					require.NoError(t, err)
					require.Equal(t, current.Role, cached.Role)
					if tc.grantRole > tc.initialRole {
						require.True(t, bundle.GitHubRolePromoted)
						require.Equal(t, int64(2), current.AuthVersion)
						_, _, err = ValidateLoginSession(oldIdentity)
						require.Error(t, err, "previous sessions must not inherit promoted privileges")
					}
				})
			}
		})
	}
}

func TestGitHubPromotionInvalidatesRedisCachedSessions(t *testing.T) {
	useTestSessionSecret(t)
	user := setupAuthSessionTestDB(t)
	_, _, _, _ = useIndependentAuthSessionRedis(t)
	require.NoError(t, model.DB.Model(user).Update("github_id", "42").Error)
	before, err := CreateLoginSession(user.Id, "password", "127.0.0.1", "test")
	require.NoError(t, err)
	identity, err := ParseAccessToken(before.AccessToken)
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	require.NoError(t, err)
	oldOptions := common.OptionMap
	raw := `{"enabled":true,"user_ids":["42"],"role":100}`
	common.OptionMap = map[string]string{common.GitHubAccessPolicyKey: raw}
	t.Cleanup(func() { common.OptionMap = oldOptions })
	after, err := CreateLoginSessionAtAuthVersion(user.Id, user.AuthVersion, "oauth:github", "127.0.0.1", "test", &common.GitHubLoginGrant{UserID: "42", Policy: raw})
	require.NoError(t, err)
	_, _, err = ValidateLoginSession(identity)
	require.Error(t, err)
	newIdentity, err := ParseAccessToken(after.AccessToken)
	require.NoError(t, err)
	_, cached, err := ValidateLoginSession(newIdentity)
	require.NoError(t, err)
	require.Equal(t, common.RoleRootUser, cached.Role)
}
