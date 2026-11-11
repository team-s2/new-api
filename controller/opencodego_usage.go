package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetOpenCodeGoChannelUsage returns the OpenCode Go subscription quota windows
// (5-hour rolling, weekly and monthly) for one channel.
func GetOpenCodeGoChannelUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

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
	if ch.Type != constant.ChannelTypeOpenCodeGoSub {
		common.ApiErrorT(c, "Channel type is not OpenCode Go Subscription")
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels are not supported")
		return
	}

	key, _, apiErr := ch.GetNextEnabledKey()
	if apiErr != nil {
		common.ApiErrorT(c, "No enabled channel key")
		return
	}
	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	usage, upstreamStatus, err := service.FetchOpenCodeGoUsage(c.Request.Context(), client, ch.GetBaseURL(), strings.TrimSpace(key))
	if err != nil {
		common.SysError(fmt.Sprintf("failed to fetch OpenCode Go usage for channel %d: %s", ch.Id, err.Error()))
		messageKey := openCodeGoUsageErrorMessage(upstreamStatus)
		message := messageKey
		if upstreamStatus != 0 {
			// Keep the status in the rendered text for non-console callers such
			// as the QQ bot; the console translates message_key instead.
			message = fmt.Sprintf("%s (HTTP %d)", messageKey, upstreamStatus)
		}
		c.JSON(http.StatusOK, gin.H{
			"success":         false,
			"message":         message,
			"message_key":     messageKey,
			"upstream_status": upstreamStatus,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"message":         "",
		"upstream_status": upstreamStatus,
		"data":            usage,
	})
}

// openCodeGoUsageErrorMessage maps the upstream status to a message the console
// and the QQ bot can present without echoing upstream response bodies.
func openCodeGoUsageErrorMessage(status int) string {
	switch status {
	case 0:
		return "Failed to reach OpenCode Go; check the network and proxy settings"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "OpenCode Go rejected the credential; check the API key and subscription"
	case http.StatusTooManyRequests:
		return "OpenCode Go is rate limiting; try again later"
	}
	if status >= http.StatusInternalServerError {
		return "OpenCode Go is temporarily unavailable; try again later"
	}
	return "Failed to fetch OpenCode Go usage"
}
