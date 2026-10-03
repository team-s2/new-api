package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ApplyGitHubLoginGrantWithTx runs only after all required login factors succeed.
// Promotion invalidates old sessions; an automatic grant never demotes a user.
func ApplyGitHubLoginGrantWithTx(tx *gorm.DB, session *UserSession, grant common.GitHubLoginGrant) error {
	policy, raw, err := common.CurrentGitHubAccessPolicy()
	if err != nil || raw != grant.Policy || grant.UserID == "" {
		return ErrAuthFlowInvalid
	}
	var user User
	if err := lockForUpdate(tx).First(&user, session.UserID).Error; err != nil {
		return err
	}
	if user.GitHubId != grant.UserID || user.Status != common.UserStatusEnabled || user.AuthVersion != session.UserAuthVersion {
		return ErrUserSessionInactive
	}
	if !policy.Enabled || policy.Role <= user.Role {
		return nil
	}
	version, err := IncrementUserAuthVersionWithTx(tx, user.Id)
	if err != nil {
		return err
	}
	if err := tx.Model(&user).Update("role", policy.Role).Error; err != nil {
		return err
	}
	session.UserAuthVersion = version
	return nil
}

func CreateGitHubUserSession(session *UserSession, grant common.GitHubLoginGrant) error {
	cacheDeadline := userSessionCacheDeadline()
	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ApplyGitHubLoginGrantWithTx(tx, session, grant); err != nil {
			return err
		}
		now := time.Now().Unix()
		var activeCount, issuanceCount int64
		if err := tx.Model(&UserSession{}).Where("user_id = ? AND status = ? AND expires_at > ?", session.UserID, UserSessionStatusActive, now).Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount >= int64(common.UserSessionActiveLimit) {
			return ErrUserSessionLimit
		}
		if err := tx.Model(&UserSession{}).Where("user_id = ? AND created_at > ?", session.UserID, now-common.UserSessionIssuanceWindowSeconds).Count(&issuanceCount).Error; err != nil {
			return err
		}
		if issuanceCount >= int64(common.UserSessionIssuanceLimit) {
			return ErrUserSessionIssuanceLimit
		}
		return createUserSessionWithTx(tx, session)
	}); err != nil {
		return err
	}
	if err := PublishUserAuthCache(session.UserID); err != nil {
		return err
	}
	return publishCreatedUserSession(session, cacheDeadline)
}
