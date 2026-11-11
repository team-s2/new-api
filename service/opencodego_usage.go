package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// OpenCode Go subscription probes. The Go gateway exposes the same model
// catalog and quota windows to every subscription; inference uses sibling
// protocol endpoints handled by relay/channel/opencodego.
const (
	openCodeGoUsagePath  = "/v1/usage"
	openCodeGoModelsPath = "/v1/models"

	openCodeGoProbeTimeout    = 15 * time.Second
	openCodeGoMaxResponseSize = 1 << 20
)

// OpenCodeGoUsageWindow is one normalized allowance window. Percent is the
// used fraction reported by the gateway, from 0 to 100.
type OpenCodeGoUsageWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
}

// OpenCodeGoUsage is the subscription view rendered by the console and the QQ
// bot. Rolling is the 5-hour window; weekly and monthly are calendar windows.
type OpenCodeGoUsage struct {
	Rolling   OpenCodeGoUsageWindow `json:"rolling"`
	Weekly    OpenCodeGoUsageWindow `json:"weekly"`
	Monthly   OpenCodeGoUsageWindow `json:"monthly"`
	FetchedAt string                `json:"fetched_at"`
}

type openCodeGoUsageWindowPayload struct {
	Status   string   `json:"status"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resetsAt"`
}

type openCodeGoUsagePayload struct {
	Usage *struct {
		Rolling *openCodeGoUsageWindowPayload `json:"rolling"`
		Weekly  *openCodeGoUsageWindowPayload `json:"weekly"`
		Monthly *openCodeGoUsageWindowPayload `json:"monthly"`
	} `json:"usage"`
}

// FetchOpenCodeGoUsage reads the authenticated quota windows. The HTTP status
// is returned so callers can distinguish an expired credential from a
// transient gateway failure without parsing the error text.
func FetchOpenCodeGoUsage(ctx context.Context, client *http.Client, baseURL, apiKey string) (*OpenCodeGoUsage, int, error) {
	status, body, err := getOpenCodeGo(ctx, client, baseURL, openCodeGoUsagePath, apiKey)
	if err != nil {
		return nil, status, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, status, openCodeGoUpstreamError(status, body, apiKey)
	}

	var payload openCodeGoUsagePayload
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, status, errors.New("opencode go usage response is not valid JSON")
	}
	if payload.Usage == nil {
		return nil, status, errors.New("opencode go usage response is missing usage")
	}

	usage := &OpenCodeGoUsage{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	windows := []struct {
		name    string
		payload *openCodeGoUsageWindowPayload
		target  *OpenCodeGoUsageWindow
	}{
		{"rolling", payload.Usage.Rolling, &usage.Rolling},
		{"weekly", payload.Usage.Weekly, &usage.Weekly},
		{"monthly", payload.Usage.Monthly, &usage.Monthly},
	}
	for _, window := range windows {
		normalized, err := normalizeOpenCodeGoWindow(window.name, window.payload)
		if err != nil {
			return nil, status, err
		}
		*window.target = *normalized
	}
	return usage, status, nil
}

func normalizeOpenCodeGoWindow(name string, payload *openCodeGoUsageWindowPayload) (*OpenCodeGoUsageWindow, error) {
	if payload == nil || payload.Percent == nil {
		return nil, fmt.Errorf("opencode go usage response is missing the %s window", name)
	}
	status := strings.TrimSpace(payload.Status)
	if status != "ok" && status != "rate-limited" {
		return nil, fmt.Errorf("opencode go %s window has unknown status %q", name, payload.Status)
	}
	if *payload.Percent < 0 || *payload.Percent > 100 {
		return nil, fmt.Errorf("opencode go %s window percent is out of range", name)
	}
	resetsAt := strings.TrimSpace(payload.ResetsAt)
	resetTime, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil {
		return nil, fmt.Errorf("opencode go %s window has an invalid reset time", name)
	}
	return &OpenCodeGoUsageWindow{
		Status:   status,
		Percent:  *payload.Percent,
		ResetsAt: resetTime.UTC().Format(time.RFC3339),
	}, nil
}

// FetchOpenCodeGoChannelModels lists the chat models the channel credential can
// actually use. The authenticated listing is account-scoped: models disabled by
// the workspace's privacy settings are absent.
func FetchOpenCodeGoChannelModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	status, body, err := getOpenCodeGo(ctx, client, baseURL, openCodeGoModelsPath, apiKey)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, openCodeGoUpstreamError(status, body, apiKey)
	}

	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Status     string `json:"status"`
			Deprecated bool   `json:"deprecated"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("opencode go model listing is not valid JSON")
	}

	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || !strings.EqualFold(strings.TrimSpace(item.Type), "chat") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Status), "deprecated") || item.Deprecated {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	slices.Sort(models)
	return models, nil
}

func getOpenCodeGo(ctx context.Context, client *http.Client, baseURL, path, apiKey string) (int, []byte, error) {
	baseURL = constant.NormalizeOpenCodeGoBaseURL(baseURL)
	if client == nil {
		return 0, nil, errors.New("opencode go client is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, openCodeGoProbeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "new-api/"+strings.TrimPrefix(common.Version, "v"))

	// A credential-bearing probe must never follow a redirect to another host,
	// because net/http forwards the Authorization header to the new location.
	probeClient := *client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("opencode go request failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, openCodeGoMaxResponseSize+1))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("failed to read opencode go response: %w", err)
	}
	if len(body) > openCodeGoMaxResponseSize {
		return response.StatusCode, nil, errors.New("opencode go response exceeds the size limit")
	}
	return response.StatusCode, body, nil
}

// openCodeGoUpstreamError keeps a short, credential-free reason from the
// gateway. The status is authoritative; the body is only used when it carries a
// readable message.
func openCodeGoUpstreamError(status int, body []byte, apiKey string) error {
	message := ""
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Data    struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if common.Unmarshal(body, &payload) == nil {
		for _, candidate := range []string{payload.Error.Message, payload.Error.Data.Message, payload.Message} {
			if strings.TrimSpace(candidate) != "" {
				message = strings.TrimSpace(candidate)
				break
			}
		}
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
	}
	message = strings.Join(strings.Fields(message), " ")
	message = strings.ReplaceAll(message, "<", "")
	message = strings.ReplaceAll(message, ">", "")
	if utf8.RuneCountInString(message) > 200 {
		message = string([]rune(message)[:200]) + "…"
	}
	if message == "" {
		return fmt.Errorf("opencode go gateway returned HTTP %d", status)
	}
	return fmt.Errorf("opencode go gateway returned HTTP %d: %s", status, message)
}
