package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qqbot/render"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// operatorHeader carries the QQ sender into audit records. Only this package
// can reach the engine below, so the header cannot be spoofed from outside.
const operatorHeader = "X-QQ-Bot-Operator"

// newAPIEngine mounts the management handlers the bot uses on a private engine.
// Going through the real handlers keeps their validation, cache refreshes,
// channel WebSocket teardown and audit logs, without a network hop or an access
// token. A session binding is the bot's only authorization, as in the former
// AstrBot plugin, so requests run with the root role.
func newAPIEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(middleware.PanicRecover(), func(c *gin.Context) {
		c.Set("id", system_setting.GetQQBotSettings().UserID)
		c.Set("role", common.RoleRootUser)
		c.Set("username", c.GetHeader(operatorHeader))
	})
	api := engine.Group("/api")
	api.GET("/channel/", controller.GetAllChannels)
	api.PUT("/channel/", controller.UpdateChannel)
	api.POST("/channel/:id/status", controller.UpdateChannelStatus)
	api.GET("/channel/:id/codex/usage", controller.GetCodexChannelUsage)
	api.GET("/channel/:id/codex/usage/reset-credits", controller.GetCodexChannelRateLimitResetCredits)
	api.GET("/channel/:id/zhipu/coding-plan/usage", controller.GetZhipuCodingPlanUsage)
	api.GET("/channel/:id/grok/usage", controller.GetGrokChannelUsage)
	api.GET("/channel/:id/opencode-go/usage", controller.GetOpenCodeGoChannelUsage)
	api.GET("/data/flow", controller.GetAllFlowQuotaDates)
	return engine
}

// responseBuffer is the in-memory http.ResponseWriter handlers write into.
type responseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseBuffer) Header() http.Header { return r.header }

func (r *responseBuffer) Write(data []byte) (int, error) { return r.body.Write(data) }

func (r *responseBuffer) WriteHeader(status int) { r.status = status }

// apiClient calls the management handlers on behalf of one QQ sender.
type apiClient struct {
	engine   *gin.Engine
	operator string
	timeout  time.Duration
}

func (c *apiClient) request(ctx context.Context, method, path string, query url.Values, payload any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body bytes.Buffer
	if payload != nil {
		encoded, err := common.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body.Write(encoded)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, path, &body)
	if err != nil {
		return nil, err
	}
	request.RemoteAddr = "127.0.0.1:0"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(operatorHeader, c.operator)
	response := &responseBuffer{header: http.Header{}, status: http.StatusOK}
	c.engine.ServeHTTP(response, request)
	if ctx.Err() != nil {
		return nil, errors.New("cannot connect to new-api: timeout")
	}

	var envelope struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := common.Unmarshal(response.body.Bytes(), &envelope); err != nil {
		return nil, fmt.Errorf("new-api returned HTTP %d: invalid response", response.status)
	}
	if response.status >= http.StatusBadRequest {
		message := envelope.Message
		if message == "" {
			message = "new-api request failed"
		}
		return nil, fmt.Errorf("HTTP %d: %s", response.status, message)
	}
	if !envelope.Success {
		if envelope.Message == "" {
			return nil, errors.New("new-api request failed")
		}
		return nil, errors.New(envelope.Message)
	}
	return envelope.Data, nil
}

// channel is the part of a channel record the bot reads.
type channel struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Type     int     `json:"type"`
	Status   int     `json:"status"`
	Weight   *int64  `json:"weight"`
	Priority *int64  `json:"priority"`
	Group    string  `json:"group"`
	BaseURL  *string `json:"base_url"`
	Setting  *string `json:"setting"`
}

func (c *apiClient) allChannels(ctx context.Context) ([]channel, error) {
	var channels []channel
	seen := map[int]bool{}
	for page := 1; ; page++ {
		query := url.Values{"p": {strconv.Itoa(page)}, "page_size": {"100"}}
		data, err := c.request(ctx, http.MethodGet, "/api/channel/", query, nil)
		if err != nil {
			return nil, err
		}
		var list struct {
			Items []channel `json:"items"`
			Total int       `json:"total"`
		}
		if err := common.Unmarshal(data, &list); err != nil {
			return nil, errors.New("new-api returned an invalid channel list")
		}
		added := 0
		for _, item := range list.Items {
			if !seen[item.ID] {
				seen[item.ID] = true
				channels = append(channels, item)
				added++
			}
		}
		if len(channels) >= list.Total {
			return channels, nil
		}
		if added == 0 {
			return nil, errors.New("渠道分页未返回新数据，请重试")
		}
	}
}

// updateChannelStatus sets a channel to enabled (1) or manually disabled (2)
// and reports whether the status changed.
func (c *apiClient) updateChannelStatus(ctx context.Context, id, status int) (bool, error) {
	path := fmt.Sprintf("/api/channel/%d/status", id)
	data, err := c.request(ctx, http.MethodPost, path, nil, map[string]any{"status": status})
	if err != nil {
		return false, err
	}
	var changed bool
	if err := common.Unmarshal(data, &changed); err != nil {
		return false, errors.New("new-api returned an invalid channel status result")
	}
	return changed, nil
}

// updateChannelField patches one routing field; the handler keeps the rest of
// the record, so credentials and model lists are never touched.
func (c *apiClient) updateChannelField(ctx context.Context, id int, field string, value int64) error {
	_, err := c.request(ctx, http.MethodPut, "/api/channel/", nil, map[string]any{"id": id, field: value})
	return err
}

// updateChannelSetting replaces the channel's setting JSON.
func (c *apiClient) updateChannelSetting(ctx context.Context, id int, setting string) error {
	_, err := c.request(ctx, http.MethodPut, "/api/channel/", nil, map[string]any{"id": id, "setting": setting})
	return err
}

// usage fetches a subscription usage payload from a channel's usage path.
func (c *apiClient) usage(ctx context.Context, id int, suffix string) (map[string]any, error) {
	data, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/api/channel/%d/%s", id, suffix), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil || payload == nil {
		return nil, errors.New("new-api returned invalid usage data")
	}
	return payload, nil
}

func (c *apiClient) flow(ctx context.Context, start, end int64) ([]render.FlowRow, error) {
	query := url.Values{
		"start_timestamp": {strconv.FormatInt(start, 10)},
		"end_timestamp":   {strconv.FormatInt(end, 10)},
	}
	data, err := c.request(ctx, http.MethodGet, "/api/data/flow", query, nil)
	if err != nil {
		return nil, err
	}
	var rows []render.FlowRow
	if err := common.Unmarshal(data, &rows); err != nil {
		return nil, errors.New("new-api returned invalid flow data")
	}
	return rows, nil
}
