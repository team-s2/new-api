package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type antigravityOAuthExchangeRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
	Callback  string `json:"callback"`
	Proxy     string `json:"proxy"`
}

func StartAntigravityOAuthLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	ownerSession := strings.TrimSpace(c.GetString("session_id"))
	if ownerSession == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid admin session, please sign in again",
		})
		return
	}

	session, err := service.StartAntigravityOAuthLogin(c.Request.Context(), ownerSession)
	if err != nil {
		common.SysError("failed to start antigravity oauth login: " + err.Error())
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "Failed to initialize Antigravity OAuth login session",
		})
		return
	}

	recordManageAudit(c, "channel.antigravity_oauth_login_start", map[string]any{
		"expires_in": session.ExpiresIn,
	})
	common.ApiSuccess(c, session)
}

func ExchangeAntigravityOAuthCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	ownerSession := strings.TrimSpace(c.GetString("session_id"))
	if ownerSession == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid admin session, please sign in again",
		})
		return
	}

	var req antigravityOAuthExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request payload",
		})
		return
	}

	callbackInput := strings.TrimSpace(req.Input)
	if callbackInput == "" {
		callbackInput = strings.TrimSpace(req.Callback)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	result, err := service.ExchangeAntigravityOAuthCode(ctx, ownerSession, req.SessionID, callbackInput, req.Proxy)
	if err != nil {
		msg := "Failed to exchange Antigravity authorization code"
		reason := "upstream_error"
		switch {
		case errors.Is(err, service.ErrAntigravityOAuthSessionNotFound):
			msg = "OAuth login session expired or already used, please start login again"
			reason = "session_not_found"
		case errors.Is(err, service.ErrAntigravityOAuthSessionOwnerMismatch):
			msg = "OAuth login session does not belong to current admin session, please start login again"
			reason = "session_owner_mismatch"
		case errors.Is(err, service.ErrAntigravityOAuthStateMismatch):
			msg = "OAuth state verification failed, please paste the full callback URL from the current login attempt"
			reason = "state_mismatch"
		case errors.Is(err, service.ErrAntigravityOAuthCodeEmpty):
			msg = "Authorization code is empty"
			reason = "code_empty"
		case errors.Is(err, service.ErrAntigravityOAuthInvalidInput):
			msg = err.Error()
			reason = "invalid_input"
		default:
			common.SysError("antigravity oauth exchange failed: " + err.Error())
		}
		recordManageAudit(c, "channel.antigravity_oauth_exchange_failed", map[string]any{
			"reason": reason,
		})
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": msg,
		})
		return
	}

	recordManageAudit(c, "channel.antigravity_oauth_exchange_success", map[string]any{
		"has_refresh_token": result.Key.RefreshToken != "",
		"has_project_id":    result.Key.ProjectID != "",
		"plan_tier":         result.Key.PlanTier,
		"expires_at":        result.Key.Expired,
	})

	common.ApiSuccess(c, gin.H{
		"credential":        result.Credential,
		"expires_at":        result.Key.Expired,
		"last_refresh":      result.Key.LastRefresh,
		"project_id":        result.Key.ProjectID,
		"email":             result.Key.Email,
		"plan_tier":         result.Key.PlanTier,
		"has_refresh_token": result.Key.RefreshToken != "",
	})
}

func RefreshAntigravityChannelCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	oauthKey, ch, err := service.RefreshAntigravityChannelCredential(ctx, channelId)
	if err != nil {
		common.SysError("failed to refresh antigravity channel credential: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to refresh Antigravity credential, please try again later"})
		return
	}

	recordManageAudit(c, "channel.antigravity_credential_refresh", map[string]any{
		"channel_id": channelId,
		"expires_at": oauthKey.Expired,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "refreshed",
		"data": gin.H{
			"expires_at":   oauthKey.Expired,
			"last_refresh": oauthKey.LastRefresh,
			"project_id":   oauthKey.ProjectID,
			"email":        oauthKey.Email,
			"plan_tier":    oauthKey.PlanTier,
			"channel_id":   ch.Id,
			"channel_type": ch.Type,
			"channel_name": ch.Name,
		},
	})
}

func GetAntigravityChannelUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if ch.Type != constant.ChannelTypeAntigravitySub {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Only Antigravity Subscription channels are supported"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	key := ch.Key
	if !ch.ChannelInfo.IsMultiKey {
		if refreshedKey, ensureErr := service.EnsureAntigravityChannelAccessToken(ctx, ch); ensureErr == nil {
			key = refreshedKey
		}
	}

	proxyURL := strings.TrimSpace(ch.GetSetting().Proxy)
	usage, err := service.FetchAntigravityUsage(ctx, ch.GetBaseURL(), key, proxyURL)
	if err != nil {
		common.SysError("failed to fetch antigravity channel usage: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to fetch Antigravity subscription usage"})
		return
	}

	var keyMeta gin.H
	if parsed, ok := service.ParseAntigravityOAuthKey(key); ok {
		keyMeta = gin.H{
			"expires_at":        parsed.Expired,
			"last_refresh":      parsed.LastRefresh,
			"project_id":        parsed.ProjectID,
			"email":             parsed.Email,
			"plan_tier":         parsed.PlanTier,
			"has_refresh_token": parsed.RefreshToken != "",
		}
	}

	common.ApiSuccess(c, gin.H{
		"channel_id":   ch.Id,
		"channel_name": ch.Name,
		"channel_type": ch.Type,
		"credential":   keyMeta,
		"usage":        usage,
	})
}
