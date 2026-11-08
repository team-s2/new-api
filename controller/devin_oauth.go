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

type devinOAuthExchangeRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input"`
	APIKey    string `json:"api_key"`
}

// StartDevinOAuthLogin returns the app.devin.ai PKCE authorization URL for
// the Devin Subscription channel. The caller opens the URL in a browser,
// signs in, and pastes the displayed authorization code into
// ExchangeDevinOAuthCode.
func StartDevinOAuthLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	result, err := service.StartDevinOAuthLogin(c.GetString("session_id"))
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

// ExchangeDevinOAuthCode swaps the pasted authorization code for a complete
// Devin Subscription channel credential. An optional durable cog_* api_key
// may be attached for credential self-healing.
func ExchangeDevinOAuthCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var request devinOAuthExchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if strings.TrimSpace(request.SessionID) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "session_id is required, please restart OAuth login"})
		return
	}
	if strings.TrimSpace(request.Input) == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "input is required, paste the authorization code"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	credential, err := service.ExchangeDevinOAuthCode(ctx, request.SessionID, request.Input, "", c.GetString("session_id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	// Optionally merge a durable api_key into the OAuth credential.
	if apiKey := strings.TrimSpace(request.APIKey); apiKey != "" {
		if cred, err := service.ParseDevinCredential(credential); err == nil {
			cred.APIKey = apiKey
			if merged, err := common.Marshal(cred); err == nil {
				credential = string(merged)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"credential": credential,
		},
	})
}

// RefreshDevinChannelCredential re-mints the channel's session token from its
// durable api_key when one is stored. OAuth-only credentials cannot be
// refreshed and require logging in again.
func RefreshDevinChannelCredential(c *gin.Context) {
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
	if ch.Type != constant.ChannelTypeDevinSub {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not Devin Subscription"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	cred, err := service.ParseDevinCredential(ch.Key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if cred.APIKey == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel has no durable api_key; please log in again via OAuth"})
		return
	}
	if _, err := service.EnsureDevinChannelSessionToken(ctx, ch); err != nil {
		common.SysError("failed to refresh devin channel credential: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to refresh credential, please try again later"})
		return
	}

	refreshed, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	newCred, _ := service.ParseDevinCredential(refreshed.Key)
	data := gin.H{
		"channel_id":   ch.Id,
		"channel_type": ch.Type,
		"channel_name": ch.Name,
	}
	if newCred != nil {
		data["user_name"] = newCred.UserName
		data["user_id"] = newCred.UserID
		data["org_id"] = newCred.OrgID
		data["created_at"] = newCred.CreatedAt
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "refreshed",
		"data":    data,
	})
}

// GetDevinChannelUsage probes Devin's status and model-catalog RPCs for the
// channel's plan, quota, and available SWE-2 model tiers.
func GetDevinChannelUsage(c *gin.Context) {
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
	if ch.Type != constant.ChannelTypeDevinSub {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not Devin Subscription"})
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	key, err := service.EnsureDevinChannelSessionToken(c.Request.Context(), ch)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	cred, err := service.ParseDevinCredential(key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	usage, err := service.FetchDevinUsage(ctx, cred, ch.GetBaseURL(), ch.GetSetting().Proxy)
	if err != nil {
		common.SysError("failed to fetch devin usage: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to fetch usage info, please try again later"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usage,
	})
}
