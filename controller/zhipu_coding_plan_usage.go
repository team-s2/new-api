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
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": usage})
}
