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
	"github.com/QuantumNous/new-api/relay/channel/zhipu_4v"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func GetZhipuCodingPlanUsage(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if channel == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return
	}
	isBigModelSub := channel.Type == constant.ChannelTypeBigModelSub
	isLegacyCodingPlan := channel.Type == constant.ChannelTypeZhipu_v4 && strings.TrimSpace(channel.GetBaseURL()) == "glm-coding-plan"
	if !isBigModelSub && !isLegacyCodingPlan {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel is not a Zhipu Coding Plan channel"})
		return
	}
	if channel.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	if isBigModelSub {
		getBigModelSubscriptionUsage(c, channel)
		return
	}

	credential, err := zhipu_4v.ParseCodingPlanCredential(channel.Key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if credential.AccountUsername == "" || credential.AccountPassword == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Zhipu Coding Plan account username and password are required"})
		return
	}

	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	usage, err := service.FetchZhipuCodingPlanUsage(ctx, client, credential.AccountUsername, credential.AccountPassword)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to fetch Zhipu Coding Plan usage for channel %d: %v", channel.Id, err))
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	// Reset cards need the OAuth credential's zcode JWT; the legacy
	// username/password channel can never query them. Report that instead of
	// silently omitting the section.
	usage.ResetUnavailableReason = "reset cards require the OAuth credential of a BigModel Coding Plan channel"
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": usage})
}

func getBigModelSubscriptionUsage(c *gin.Context, channel *model.Channel) {
	credential, err := zhipu_4v.ParseOAuthCredential(channel.Key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	usage, err := service.FetchZhipuCodingPlanUsageWithToken(ctx, client, credential.AccessToken)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to fetch BigModel Subscription usage for channel %d: %v", channel.Id, err))
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	attachBigModelResetStatus(c, ctx, client, channel, credential, usage)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": usage})
}

// attachBigModelResetStatus fills usage.Reset on a best-effort basis: the
// quota display must keep working when the credential predates zcode_jwt
// persistence or the reset endpoint is unavailable.
func attachBigModelResetStatus(c *gin.Context, ctx context.Context, client *http.Client, channel *model.Channel, credential *zhipu_4v.OAuthCredential, usage *service.ZhipuCodingPlanUsage) {
	reset, err := service.FetchZhipuCodingPlanResetStatus(ctx, client, credential.ZcodeJWT, credential.AccessToken)
	if err != nil {
		if errors.Is(err, service.ErrZhipuResetCredentialUnsupported) {
			usage.ResetUnavailableReason = err.Error()
			return
		}
		common.SysError(fmt.Sprintf("failed to fetch BigModel reset cards for channel %d: %v", channel.Id, err))
		usage.ResetUnavailableReason = "failed to fetch reset cards, please retry later"
		return
	}
	usage.Reset = reset
}

func loadBigModelSubscriptionChannel(c *gin.Context) (*model.Channel, *zhipu_4v.OAuthCredential, *http.Client, context.Context, context.CancelFunc, bool) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return nil, nil, nil, nil, nil, false
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiError(c, err)
		return nil, nil, nil, nil, nil, false
	}
	if channel == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return nil, nil, nil, nil, nil, false
	}
	if channel.Type != constant.ChannelTypeBigModelSub {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel is not a BigModel Coding Plan channel"})
		return nil, nil, nil, nil, nil, false
	}
	if channel.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return nil, nil, nil, nil, nil, false
	}
	credential, err := zhipu_4v.ParseOAuthCredential(channel.Key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return nil, nil, nil, nil, nil, false
	}
	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return nil, nil, nil, nil, nil, false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	return channel, credential, client, ctx, cancel, true
}

func GetZhipuCodingPlanResetStatus(c *gin.Context) {
	_, credential, client, ctx, cancel, ok := loadBigModelSubscriptionChannel(c)
	if !ok {
		return
	}
	defer cancel()
	status, err := service.FetchZhipuCodingPlanResetStatus(ctx, client, credential.ZcodeJWT, credential.AccessToken)
	if err != nil {
		common.SysError("failed to fetch Zhipu Coding Plan reset status: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": status})
}

func UseZhipuCodingPlanReset(c *gin.Context) {
	var request struct {
		ResetType string `json:"reset_type"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	channel, credential, client, ctx, cancel, ok := loadBigModelSubscriptionChannel(c)
	if !ok {
		return
	}
	defer cancel()
	if err := service.UseZhipuCodingPlanReset(ctx, client, credential.ZcodeJWT, credential.AccessToken, request.ResetType); err != nil {
		common.SysError(fmt.Sprintf("failed to use Zhipu Coding Plan reset card for channel %d: %v", channel.Id, err))
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"used": true}})
}
