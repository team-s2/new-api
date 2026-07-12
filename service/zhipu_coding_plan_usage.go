package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const zhipuConsoleBaseURL = "https://open.bigmodel.cn"

type ZhipuCodingPlanUsage struct {
	Level      string                     `json:"level,omitempty"`
	FiveHour   *ZhipuCodingPlanUsageLimit `json:"five_hour,omitempty"`
	Weekly     *ZhipuCodingPlanUsageLimit `json:"weekly,omitempty"`
	MCPMonthly *ZhipuCodingPlanUsageLimit `json:"mcp_monthly,omitempty"`
}

type ZhipuCodingPlanUsageLimit struct {
	Usage         *int64                       `json:"usage,omitempty"`
	CurrentValue  *int64                       `json:"current_value,omitempty"`
	Remaining     *int64                       `json:"remaining,omitempty"`
	Percentage    int                          `json:"percentage"`
	NextResetTime int64                        `json:"next_reset_time,omitempty"`
	UsageDetails  []ZhipuCodingPlanUsageDetail `json:"usage_details,omitempty"`
}

type ZhipuCodingPlanUsageDetail struct {
	ModelCode string `json:"model_code"`
	Usage     int64  `json:"usage"`
}

type zhipuResponse[T any] struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Data    T      `json:"data"`
}

type zhipuLoginData struct {
	AccessToken string `json:"access_token"`
}

type zhipuCustomerInfo struct {
	Organizations []zhipuOrganization `json:"organizations"`
}

type zhipuOrganization struct {
	OrganizationID string         `json:"organizationId"`
	IsDefault      bool           `json:"isDefault"`
	Projects       []zhipuProject `json:"projects"`
}

type zhipuProject struct {
	ProjectID string `json:"projectId"`
	IsDefault bool   `json:"isDefault"`
}

type zhipuQuotaData struct {
	Level  string            `json:"level"`
	Limits []zhipuQuotaLimit `json:"limits"`
}

type zhipuQuotaLimit struct {
	Type          string                  `json:"type"`
	Unit          int                     `json:"unit"`
	Number        int                     `json:"number"`
	Usage         *int64                  `json:"usage"`
	CurrentValue  *int64                  `json:"currentValue"`
	Remaining     *int64                  `json:"remaining"`
	Percentage    int                     `json:"percentage"`
	NextResetTime int64                   `json:"nextResetTime"`
	UsageDetails  []zhipuQuotaUsageDetail `json:"usageDetails"`
}

type zhipuQuotaUsageDetail struct {
	ModelCode string `json:"modelCode"`
	Usage     int64  `json:"usage"`
}

func FetchZhipuCodingPlanUsage(ctx context.Context, client *http.Client, username string, password string) (*ZhipuCodingPlanUsage, error) {
	return fetchZhipuCodingPlanUsage(ctx, client, zhipuConsoleBaseURL, username, password)
}

func fetchZhipuCodingPlanUsage(ctx context.Context, client *http.Client, baseURL string, username string, password string) (*ZhipuCodingPlanUsage, error) {
	if client == nil {
		return nil, errors.New("zhipu coding plan: nil http client")
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, errors.New("zhipu coding plan: account username and password are required")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("zhipu coding plan: empty console base url")
	}

	loginBody, err := common.Marshal(map[string]string{
		"username":  username,
		"password":  password,
		"loginType": "password",
		"grantType": "customer",
		"userType":  "PERSONAL",
	})
	if err != nil {
		return nil, err
	}
	var login zhipuResponse[zhipuLoginData]
	if err := doZhipuConsoleRequest(ctx, client, http.MethodPost, baseURL+"/api/auth/login", loginBody, nil, &login); err != nil {
		return nil, fmt.Errorf("zhipu coding plan login failed: %w", err)
	}
	accessToken := strings.TrimSpace(login.Data.AccessToken)
	if !login.Success || accessToken == "" {
		return nil, fmt.Errorf("zhipu coding plan login failed: %s", zhipuMessage(login.Msg))
	}

	headers := map[string]string{"Authorization": accessToken}
	var customer zhipuResponse[zhipuCustomerInfo]
	if err := doZhipuConsoleRequest(ctx, client, http.MethodGet, baseURL+"/api/biz/customer/getCustomerInfo", nil, headers, &customer); err != nil {
		return nil, fmt.Errorf("zhipu coding plan account discovery failed: %w", err)
	}
	if !customer.Success {
		return nil, fmt.Errorf("zhipu coding plan account discovery failed: %s", zhipuMessage(customer.Msg))
	}

	organizationID, projectID := defaultZhipuProject(customer.Data.Organizations)
	if organizationID == "" || projectID == "" {
		return nil, errors.New("zhipu coding plan: default organization or project not found")
	}
	headers["Bigmodel-Organization"] = organizationID
	headers["Bigmodel-Project"] = projectID

	var quota zhipuResponse[zhipuQuotaData]
	if err := doZhipuConsoleRequest(ctx, client, http.MethodGet, baseURL+"/api/monitor/usage/quota/limit", nil, headers, &quota); err != nil {
		return nil, fmt.Errorf("zhipu coding plan usage query failed: %w", err)
	}
	if !quota.Success {
		return nil, fmt.Errorf("zhipu coding plan usage query failed: %s", zhipuMessage(quota.Msg))
	}

	usage := &ZhipuCodingPlanUsage{Level: quota.Data.Level}
	for _, item := range quota.Data.Limits {
		limit := &ZhipuCodingPlanUsageLimit{
			Usage:         item.Usage,
			CurrentValue:  item.CurrentValue,
			Remaining:     item.Remaining,
			Percentage:    item.Percentage,
			NextResetTime: item.NextResetTime,
		}
		for _, detail := range item.UsageDetails {
			limit.UsageDetails = append(limit.UsageDetails, ZhipuCodingPlanUsageDetail{
				ModelCode: detail.ModelCode,
				Usage:     detail.Usage,
			})
		}
		switch {
		case item.Type == "TOKENS_LIMIT" && item.Unit == 3 && item.Number == 5:
			usage.FiveHour = limit
		case item.Type == "TOKENS_LIMIT" && item.Unit == 6 && item.Number == 1:
			usage.Weekly = limit
		case item.Type == "TIME_LIMIT" && item.Unit == 5 && item.Number == 1:
			usage.MCPMonthly = limit
		}
	}
	return usage, nil
}

func doZhipuConsoleRequest[T any](ctx context.Context, client *http.Client, method string, url string, body []byte, headers map[string]string, target *zhipuResponse[T]) error {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Set-Language", "zh")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

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
		return fmt.Errorf("upstream status %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return errors.New("empty upstream response")
	}
	if err := common.Unmarshal(responseBody, target); err != nil {
		return errors.New("invalid upstream response")
	}
	return nil
}

func defaultZhipuProject(organizations []zhipuOrganization) (string, string) {
	for _, organization := range organizations {
		if !organization.IsDefault {
			continue
		}
		for _, project := range organization.Projects {
			if project.IsDefault {
				return organization.OrganizationID, project.ProjectID
			}
		}
	}
	for _, organization := range organizations {
		for _, project := range organization.Projects {
			if project.IsDefault {
				return organization.OrganizationID, project.ProjectID
			}
		}
	}
	return "", ""
}

func zhipuMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "unknown error"
	}
	return message
}
