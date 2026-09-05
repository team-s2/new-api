package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Zhipu coding-plan reset cards, mirroring the official ZCode client
// (zcode.z.ai /api/v1/coding-plan/reset). A card consumes the current 5-hour
// or weekly quota window so it resets immediately instead of waiting for the
// natural boundary. Cards expire, so each entry carries an expire_at stamp.
type ZhipuCodingPlanResetCard struct {
	ExpireAt int64 `json:"expire_at"`
}

type ZhipuCodingPlanResetHistory struct {
	UsedAt int64 `json:"used_at"`
}

type ZhipuCodingPlanResetStatus struct {
	AvailableFiveHourResets []ZhipuCodingPlanResetCard   `json:"available_five_hour_resets"`
	AvailableWeekResets     []ZhipuCodingPlanResetCard   `json:"available_week_resets"`
	LatestFiveHourReset     *ZhipuCodingPlanResetHistory `json:"latest_five_hour_reset_history,omitempty"`
	LatestWeekReset         *ZhipuCodingPlanResetHistory `json:"latest_week_reset_history,omitempty"`
	HasUnreadHistory        bool                         `json:"has_unread_history"`
}

// ErrZhipuResetCredentialUnsupported marks stored credentials that predate
// zcode_jwt persistence; the reset API needs that client token.
var ErrZhipuResetCredentialUnsupported = errors.New("zhipu coding plan: credential has no zcode JWT; run OAuth login again to enable reset cards")

const (
	ZhipuCodingPlanResetTypeFiveHour = "FIVE_HOUR"
	ZhipuCodingPlanResetTypeWeek     = "WEEK"
)

// Overridden by tests to point at a local httptest server.
var zhipuResetBaseURL = "https://zcode.z.ai"

// zhipuResetEnvelope is the zcode.z.ai business envelope: code 0 marks
// success, any other code is a stable business failure surfaced with msg.
type zhipuResetEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type zhipuResetStatusData struct {
	AvailableFiveHourResets []ZhipuCodingPlanResetCard   `json:"available_five_hour_resets"`
	AvailableWeekResets     []ZhipuCodingPlanResetCard   `json:"available_week_resets"`
	LatestFiveHourReset     *ZhipuCodingPlanResetHistory `json:"latest_five_hour_reset_history"`
	LatestWeekReset         *ZhipuCodingPlanResetHistory `json:"latest_week_reset_history"`
	HasUnreadHistory        bool                         `json:"has_unread_history"`
}

type zhipuResetUseData struct {
	Used bool `json:"used"`
}

// FetchZhipuCodingPlanResetStatus lists the reset cards the account can still
// spend, plus the most recent card usage per window.
func FetchZhipuCodingPlanResetStatus(ctx context.Context, client *http.Client, zcodeJWT string, accessToken string) (*ZhipuCodingPlanResetStatus, error) {
	if err := validateZhipuResetCredentials(zcodeJWT, accessToken); err != nil {
		return nil, err
	}
	var data zhipuResetStatusData
	if err := doZhipuResetRequest(ctx, client, http.MethodGet, zhipuResetBaseURL+"/api/v1/coding-plan/reset/status", nil, zcodeJWT, accessToken, &data); err != nil {
		return nil, err
	}
	return &ZhipuCodingPlanResetStatus{
		AvailableFiveHourResets: data.AvailableFiveHourResets,
		AvailableWeekResets:     data.AvailableWeekResets,
		LatestFiveHourReset:     data.LatestFiveHourReset,
		LatestWeekReset:         data.LatestWeekReset,
		HasUnreadHistory:        data.HasUnreadHistory,
	}, nil
}

// UseZhipuCodingPlanReset spends one reset card of the given window. The
// idempotency key is generated per call so a network retry after an upstream
// timeout cannot burn a second card unintentionally.
func UseZhipuCodingPlanReset(ctx context.Context, client *http.Client, zcodeJWT string, accessToken string, resetType string) error {
	if resetType != ZhipuCodingPlanResetTypeFiveHour && resetType != ZhipuCodingPlanResetTypeWeek {
		return fmt.Errorf("zhipu coding plan: invalid reset type %q", resetType)
	}
	if err := validateZhipuResetCredentials(zcodeJWT, accessToken); err != nil {
		return err
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("zhipu coding plan: generate idempotency key: %w", err)
	}
	body, err := common.Marshal(map[string]string{
		"idempotency_key": hex.EncodeToString(key),
		"reset_type":      resetType,
	})
	if err != nil {
		return err
	}
	var data zhipuResetUseData
	if err := doZhipuResetRequest(ctx, client, http.MethodPost, zhipuResetBaseURL+"/api/v1/coding-plan/reset/use", body, zcodeJWT, accessToken, &data); err != nil {
		return err
	}
	if !data.Used {
		return errors.New("zhipu coding plan: upstream did not consume the reset card")
	}
	return nil
}

func validateZhipuResetCredentials(zcodeJWT string, accessToken string) error {
	if strings.TrimSpace(zcodeJWT) == "" {
		return ErrZhipuResetCredentialUnsupported
	}
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("zhipu coding plan: access token is required")
	}
	return nil
}

func doZhipuResetRequest(ctx context.Context, client *http.Client, method string, url string, body []byte, zcodeJWT string, accessToken string, data any) error {
	if client == nil {
		return errors.New("zhipu coding plan: nil http client")
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(zcodeJWT))
	request.Header.Set("X-Bigmodel-Authorization", strings.TrimSpace(accessToken))
	request.Header.Set("Bigmodel-Target-Type", "PERSONAL")

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("zhipu coding plan reset: upstream status %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return errors.New("zhipu coding plan reset: empty upstream response")
	}
	var envelope zhipuResetEnvelope
	if err := common.Unmarshal(responseBody, &envelope); err != nil {
		return errors.New("zhipu coding plan reset: invalid upstream response")
	}
	if envelope.Code != 0 {
		message := strings.TrimSpace(envelope.Msg)
		if message == "" {
			message = fmt.Sprintf("business code %d", envelope.Code)
		}
		return fmt.Errorf("zhipu coding plan reset: %s", message)
	}
	if data == nil || len(envelope.Data) == 0 {
		return nil
	}
	if err := common.Unmarshal(envelope.Data, data); err != nil {
		return errors.New("zhipu coding plan reset: invalid upstream data")
	}
	return nil
}
