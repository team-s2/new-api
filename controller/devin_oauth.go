package controller

import (
	"context"
	"errors"
	"fmt"
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

func respondDevinConsoleError(c *gin.Context, err error, fallback string) {
	var message *common.Message
	if errors.As(err, &message) {
		common.ApiError(c, message)
		return
	}
	// Transport and provider errors may contain credentials, callback codes,
	// proxy passwords, or upstream response text. Never expose or log them.
	common.SysError(fallback)
	common.ApiErrorT(c, fallback)
}

// StartDevinOAuthLogin returns the app.devin.ai PKCE authorization URL for
// the Devin Subscription channel. The caller opens the URL in a browser,
// signs in, and pastes the displayed authorization code into
// ExchangeDevinOAuthCode.
func StartDevinOAuthLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	result, err := service.StartDevinOAuthLogin(c.GetString("session_id"))
	if err != nil {
		respondDevinConsoleError(c, err, "Failed to start OAuth login")
		return
	}
	common.ApiSuccess(c, gin.H{
		"authorize_url": result.AuthorizeURL,
		"session_id":    result.SessionID,
	})
}

// ExchangeDevinOAuthCode swaps the pasted authorization code for a complete
// Devin Subscription channel credential. An optional durable cog_* api_key
// may be attached for manual credential refresh.
func ExchangeDevinOAuthCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var request devinOAuthExchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorT(c, "Invalid request")
		return
	}
	if strings.TrimSpace(request.SessionID) == "" {
		common.ApiErrorT(c, "OAuth session ID is required. Please restart OAuth login")
		return
	}
	if strings.TrimSpace(request.Input) == "" {
		common.ApiErrorT(c, "Authorization code is required")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	credential, err := service.ExchangeDevinOAuthCode(ctx, request.SessionID, request.Input, "", c.GetString("session_id"))
	if err != nil {
		respondDevinConsoleError(c, err, "Failed to exchange OAuth token")
		return
	}

	if apiKey := strings.TrimSpace(request.APIKey); apiKey != "" {
		cred, err := service.ParseDevinCredential(credential)
		if err != nil {
			respondDevinConsoleError(c, err, "Failed to exchange OAuth token")
			return
		}
		cred.APIKey = apiKey
		merged, err := common.Marshal(cred)
		if err != nil {
			respondDevinConsoleError(c, err, "Failed to exchange OAuth token")
			return
		}
		credential = string(merged)
	}

	common.ApiSuccess(c, gin.H{"credential": credential})
}

// RefreshDevinChannelCredential manually mints a new session token from the
// stored api_key. OAuth-only credentials require logging in again.
func RefreshDevinChannelCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		common.ApiErrorT(c, "Invalid channel ID")
		return
	}

	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiErrorT(c, "Failed to load channel")
		return
	}
	if ch.Type != constant.ChannelTypeDevinSub {
		common.ApiErrorT(c, "Channel type is not Devin Subscription")
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	updatedKey, err := service.RefreshDevinChannelSessionToken(ctx, ch)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to refresh Devin credential for channel %d", ch.Id))
		respondDevinConsoleError(c, err, "Failed to refresh credentials. Please try again later")
		return
	}

	newCred, err := service.ParseDevinCredential(updatedKey)
	if err != nil {
		respondDevinConsoleError(c, err, "Failed to refresh credentials. Please try again later")
		return
	}
	common.ApiSuccessT(c, "Credential refreshed", gin.H{
		"channel_id":   ch.Id,
		"channel_type": ch.Type,
		"channel_name": ch.Name,
		"user_name":    newCred.UserName,
		"user_id":      newCred.UserID,
		"org_id":       newCred.OrgID,
		"created_at":   newCred.CreatedAt,
	})
}

// GetDevinChannelUsage probes Devin's status and model-catalog RPCs for the
// channel's plan, quota, and available SWE-2 model tiers.
func GetDevinChannelUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		common.ApiErrorT(c, "Invalid channel ID")
		return
	}

	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiErrorT(c, "Failed to load channel")
		return
	}
	if ch.Type != constant.ChannelTypeDevinSub {
		common.ApiErrorT(c, "Channel type is not Devin Subscription")
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return
	}

	cred, err := service.ParseDevinCredential(ch.Key)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	usage, err := service.FetchDevinUsage(ctx, cred, ch.GetBaseURL(), ch.GetSetting().Proxy)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to fetch Devin usage for channel %d", ch.Id))
		common.ApiErrorT(c, "Failed to fetch usage. Please try again later")
		return
	}
	common.ApiSuccess(c, usage)
}
