package controller

import (
	"context"
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

type grokOAuthExchangeRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
}

// StartGrokOAuthLogin returns the auth.x.ai PKCE authorization URL for the
// Grok Subscription channel. The caller opens the URL in a browser, signs in,
// waits for the redirect to the CLI loopback (127.0.0.1:56121) to fail, copies
// the address-bar URL, and pastes it into ExchangeGrokOAuthCode.
func StartGrokOAuthLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	result, err := service.StartGrokOAuthLogin(c.GetString("session_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"authorize_url": result.AuthorizeURL,
			"session_id":    result.SessionID,
		},
	})
}

// ExchangeGrokOAuthCode swaps the pasted loopback callback URL (or bare
// authorization code) for a complete Grok Subscription channel credential.
func ExchangeGrokOAuthCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var request grokOAuthExchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if strings.TrimSpace(request.SessionID) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "session_id is required, please restart OAuth login"})
		return
	}
	if strings.TrimSpace(request.Input) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "input is required, paste the redirected callback URL"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	credential, err := service.ExchangeGrokOAuthCode(ctx, request.SessionID, request.Input, "", c.GetString("session_id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"credential": credential,
		},
	})
}

// RefreshGrokChannelCredential refreshes a Grok Subscription channel's OAuth
// tokens on demand.
func RefreshGrokChannelCredential(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	oauthKey, ch, err := service.RefreshGrokChannelCredential(ctx, channelId)
	if err != nil {
		common.SysError("failed to refresh grok channel credential: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "刷新凭证失败，请稍后重试"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "refreshed",
		"data": gin.H{
			"expires_at":   oauthKey.Expired,
			"last_refresh": oauthKey.LastRefresh,
			"email":        oauthKey.Email,
			"plan_tier":    oauthKey.PlanTier,
			"channel_id":   ch.Id,
			"channel_type": ch.Type,
			"channel_name": ch.Name,
		},
	})
}

// GetGrokChannelUsage probes the Grok CLI gateway billing endpoints for the
// channel's subscription quota (weekly credits window + monthly dollar
// window). On 401 the credential is refreshed once and the probe retried.
func GetGrokChannelUsage(c *gin.Context) {
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
	if ch.Type != constant.ChannelTypeGrokSub {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not Grok Subscription"})
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	probe := func(rawKey string) (*service.GrokBillingUsage, int, error) {
		oauthKey, err := service.ParseGrokOAuthKey(rawKey)
		if err != nil {
			return nil, 0, err
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), service.GrokBillingTimeout())
		defer cancel()
		return service.FetchGrokBillingUsage(ctx, client, ch.GetBaseURL(), strings.TrimSpace(oauthKey.AccessToken))
	}

	key, err := service.EnsureGrokChannelAccessToken(c.Request.Context(), ch)
	if err != nil {
		common.SysError("failed to prepare grok credential: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "解析凭证失败，请检查渠道配置"})
		return
	}
	usage, statusCode, err := probe(key)
	if err == nil && statusCode == http.StatusUnauthorized {
		if _, refreshed, refreshErr := service.RefreshGrokChannelCredential(c.Request.Context(), ch.Id); refreshErr == nil {
			usage, statusCode, err = probe(refreshed.Key)
		} else {
			common.SysError("failed to refresh grok credential after billing 401: " + refreshErr.Error())
		}
	}
	if err != nil {
		common.SysError("failed to fetch grok usage: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取用量信息失败，请稍后重试"})
		return
	}

	resp := gin.H{
		"success":         usage != nil,
		"message":         "",
		"upstream_status": statusCode,
		"data":            usage,
	}
	if usage == nil {
		resp["message"] = fmt.Sprintf("upstream status: %d", statusCode)
	}
	c.JSON(http.StatusOK, resp)
}
