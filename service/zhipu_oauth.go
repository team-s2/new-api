package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// BigModel (Zhipu) coding plan OAuth, reverse engineered from the official
// ZCode client: the browser authorizes on bigmodel.cn through a zcode.z.ai
// relay and lands on the zcode://oauth/callback deep link; the pasted link is
// exchanged at zcode.z.ai for a bigmodel console access token.
const (
	zhipuOAuthProvider     = "bigmodel"
	zhipuOAuthAppID        = "zcode"
	zhipuOAuthAuthorizeURL = "https://bigmodel.cn/login"
	zhipuOAuthRelayURL     = "https://zcode.z.ai/app/oauth/login"
	zhipuOAuthCallbackURI  = "zcode://oauth/callback"
)

// zhipuOAuthTokenURL and zhipuConsoleBaseURL are vars so tests can point them
// at a local httptest server.
var (
	zhipuOAuthTokenURL  = "https://zcode.z.ai/api/v1/oauth/token"
	zhipuConsoleBaseURL = "https://open.bigmodel.cn"
)

// zhipuCodingPlanAPIKeyName mirrors the key the official ZCode client
// provisions in the default organization/project to relay coding plan calls.
const zhipuCodingPlanAPIKeyName = "zcode-api-key"

type ZhipuOAuthTokenSet struct {
	ZcodeJWT     string
	AccessToken  string
	RefreshToken string
	Username     string
}

type zhipuOAuthResponse struct {
	Code any    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Token    string                  `json:"token"`
		Bigmodel zhipuOAuthBigmodelToken `json:"bigmodel"`
		User     zhipuOAuthUser          `json:"user"`
	}
}

type zhipuOAuthBigmodelToken struct {
	AccessToken       string `json:"access_token"`
	AccessTokenCamel  string `json:"accessToken"`
	RefreshToken      string `json:"refresh_token"`
	RefreshTokenCamel string `json:"refreshToken"`
}

type zhipuOAuthUser struct {
	CustomerNumber string `json:"customerNumber"`
	CustomerName   string `json:"customerName"`
	NickName       string `json:"nickName"`
	Name           string `json:"name"`
	Email          string `json:"email"`
}

// zhipuBizResponse is the open.bigmodel.cn console API envelope. Success is
// signaled by code being nil/0/200 (or their string forms), mirroring the
// official client's tolerance.
type zhipuBizResponse[T any] struct {
	Code any    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

func (r *zhipuBizResponse[T]) ok() bool {
	switch v := r.Code.(type) {
	case nil:
		return true
	case float64:
		return v == 0 || v == 200
	case string:
		return v == "0" || v == "200"
	default:
		return false
	}
}

func GenerateZhipuOAuthState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("zhipu oauth: generate state: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func BuildZhipuOAuthAuthorizeURL(state string) string {
	relay := zhipuOAuthRelayURL + "?redirect=" + url.QueryEscape(zhipuOAuthCallbackURI)
	query := url.Values{}
	query.Set("redirect", relay)
	query.Set("appId", zhipuOAuthAppID)
	query.Set("state", state)
	return zhipuOAuthAuthorizeURL + "?" + query.Encode()
}

// ParseZhipuOAuthCallback accepts the full pasted callback link
// (zcode://oauth/callback?authCode=...&state=...) or a bare authorization
// code. When the input carries a state it must match the expected one.
func ParseZhipuOAuthCallback(input string, expectedState string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("zhipu oauth: empty callback input")
	}
	if !strings.Contains(trimmed, "=") {
		return trimmed, nil
	}
	raw := trimmed
	if idx := strings.Index(raw, "?"); idx >= 0 {
		raw = raw[idx+1:]
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", errors.New("zhipu oauth: invalid callback input")
	}
	code := strings.TrimSpace(values.Get("authCode"))
	if code == "" {
		code = strings.TrimSpace(values.Get("code"))
	}
	if code == "" {
		return "", errors.New("zhipu oauth: callback input does not contain an authorization code")
	}
	if state := strings.TrimSpace(values.Get("state")); state != "" && state != expectedState {
		return "", errors.New("zhipu oauth: state mismatch, please restart OAuth login and paste the new callback link")
	}
	return code, nil
}

func ExchangeZhipuOAuthCode(ctx context.Context, client *http.Client, code string, state string) (*ZhipuOAuthTokenSet, error) {
	if client == nil {
		return nil, errors.New("zhipu oauth: nil http client")
	}
	body, err := common.Marshal(map[string]string{
		"provider":     zhipuOAuthProvider,
		"code":         code,
		"redirect_uri": zhipuOAuthCallbackURI,
		"state":        state,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, zhipuOAuthTokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("zhipu oauth: token exchange failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("zhipu oauth: token exchange failed: upstream status %d", response.StatusCode)
	}
	var parsed zhipuOAuthResponse
	if err := common.Unmarshal(responseBody, &parsed); err != nil {
		return nil, errors.New("zhipu oauth: invalid token exchange response")
	}
	if !zhipuOAuthCodeOk(parsed.Code) {
		message := strings.TrimSpace(parsed.Msg)
		if message == "" {
			message = fmt.Sprintf("business code %v", parsed.Code)
		}
		return nil, fmt.Errorf("zhipu oauth: token exchange failed: %s", message)
	}

	accessToken := strings.TrimSpace(parsed.Data.Bigmodel.AccessToken)
	if accessToken == "" {
		accessToken = strings.TrimSpace(parsed.Data.Bigmodel.AccessTokenCamel)
	}
	if accessToken == "" {
		return nil, errors.New("zhipu oauth: token exchange response is missing bigmodel access_token")
	}
	refreshToken := strings.TrimSpace(parsed.Data.Bigmodel.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(parsed.Data.Bigmodel.RefreshTokenCamel)
	}
	tokenSet := &ZhipuOAuthTokenSet{
		ZcodeJWT:     strings.TrimSpace(parsed.Data.Token),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}
	tokenSet.Username = zhipuOAuthDisplayName(parsed.Data.User)
	return tokenSet, nil
}

func zhipuOAuthCodeOk(code any) bool {
	switch v := code.(type) {
	case nil:
		return true
	case float64:
		return v == 0 || v == 200
	case string:
		return v == "0" || v == "200"
	default:
		return false
	}
}

func zhipuOAuthDisplayName(user zhipuOAuthUser) string {
	for _, candidate := range []string{user.CustomerName, user.NickName, user.Name, user.Email, user.CustomerNumber} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type zhipuOAuthCustomerInfo struct {
	CustomerName   string              `json:"customerName"`
	CustomerNumber string              `json:"customerNumber"`
	Organizations  []zhipuOrganization `json:"organizations"`
}

type zhipuOAuthAPIKeyEntry struct {
	Name   string `json:"name"`
	APIKey string `json:"apiKey"`
}

// DeriveZhipuCodingPlanAPIKey provisions the coding plan API key exactly like
// the official ZCode client: resolve the default organization/project, reuse
// (or create) the "zcode-api-key" entry, then reveal its secret via the copy
// endpoint. The resulting "apiKey.secretKey" pair is the relay credential.
func DeriveZhipuCodingPlanAPIKey(ctx context.Context, client *http.Client, accessToken string) (string, *zhipuOAuthCustomerInfo, error) {
	if client == nil {
		return "", nil, errors.New("zhipu oauth: nil http client")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", nil, errors.New("zhipu oauth: access token is required to derive api key")
	}

	var customer zhipuBizResponse[zhipuOAuthCustomerInfo]
	if err := doZhipuOAuthConsoleRequest(ctx, client, http.MethodGet, zhipuConsoleBaseURL+"/api/biz/customer/getCustomerInfo", nil, accessToken, &customer); err != nil {
		return "", nil, fmt.Errorf("zhipu oauth: fetch customer info failed: %w", err)
	}
	if !customer.ok() {
		return "", nil, fmt.Errorf("zhipu oauth: fetch customer info failed: %s", zhipuOAuthMessage(customer.Msg))
	}
	organizationID, projectID := defaultZhipuProject(customer.Data.Organizations)
	if organizationID == "" || projectID == "" {
		return "", nil, errors.New("zhipu oauth: default organization or project not found")
	}

	keysURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", zhipuConsoleBaseURL, organizationID, projectID)
	var list zhipuBizResponse[[]zhipuOAuthAPIKeyEntry]
	if err := doZhipuOAuthConsoleRequest(ctx, client, http.MethodGet, keysURL, nil, accessToken, &list); err != nil {
		return "", nil, fmt.Errorf("zhipu oauth: list api keys failed: %w", err)
	}
	if !list.ok() {
		return "", nil, fmt.Errorf("zhipu oauth: list api keys failed: %s", zhipuOAuthMessage(list.Msg))
	}
	var apiKey string
	for _, entry := range list.Data {
		if entry.Name == zhipuCodingPlanAPIKeyName {
			apiKey = strings.TrimSpace(entry.APIKey)
			break
		}
	}
	if apiKey == "" {
		createBody, err := common.Marshal(map[string]string{"name": zhipuCodingPlanAPIKeyName})
		if err != nil {
			return "", nil, err
		}
		var created zhipuBizResponse[zhipuOAuthAPIKeyEntry]
		if err := doZhipuOAuthConsoleRequest(ctx, client, http.MethodPost, keysURL, createBody, accessToken, &created); err != nil {
			return "", nil, fmt.Errorf("zhipu oauth: create api key failed: %w", err)
		}
		if !created.ok() {
			return "", nil, fmt.Errorf("zhipu oauth: create api key failed: %s", zhipuOAuthMessage(created.Msg))
		}
		apiKey = strings.TrimSpace(created.Data.APIKey)
	}
	if apiKey == "" {
		return "", nil, errors.New("zhipu oauth: api key response is missing apiKey")
	}

	copyURL := fmt.Sprintf("%s/copy/%s", keysURL, url.PathEscape(apiKey))
	var copied zhipuBizResponse[struct {
		SecretKey string `json:"secretKey"`
	}]
	if err := doZhipuOAuthConsoleRequest(ctx, client, http.MethodGet, copyURL, nil, accessToken, &copied); err != nil {
		return "", nil, fmt.Errorf("zhipu oauth: reveal api key secret failed: %w", err)
	}
	if !copied.ok() {
		return "", nil, fmt.Errorf("zhipu oauth: reveal api key secret failed: %s", zhipuOAuthMessage(copied.Msg))
	}
	secretKey := strings.TrimSpace(copied.Data.SecretKey)
	if secretKey == "" {
		return "", nil, errors.New("zhipu oauth: api key copy response is missing secretKey")
	}

	info := &customer.Data
	if info.CustomerName == "" {
		info.CustomerName = customer.Data.CustomerNumber
	}
	return apiKey + "." + secretKey, info, nil
}

func doZhipuOAuthConsoleRequest[T any](ctx context.Context, client *http.Client, method string, requestURL string, body []byte, accessToken string, target *zhipuBizResponse[T]) error {
	var reader io.Reader
	if len(body) > 0 {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Set-Language", "zh")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	request.Header.Set("Authorization", accessToken)

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
	if len(strings.TrimSpace(string(responseBody))) == 0 {
		return errors.New("empty upstream response")
	}
	if err := common.Unmarshal(responseBody, target); err != nil {
		return errors.New("invalid upstream response")
	}
	return nil
}

func zhipuOAuthMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "unknown error"
	}
	return message
}
