// Package official implements the QQ official bot transport: new-api dials the
// QQ gateway with the bot's AppID and AppSecret, receives group @-mentions, and
// answers through the QQ OpenAPI. It owns transport concerns only; commands
// live in the parent package.
package official

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	tokenURL    = "https://bots.qq.com/app/getAppAccessToken"
	apiBase     = "https://api.sgroup.qq.com"
	maxResponse = 1 << 20
)

// Credentials is the bot identity the client currently runs under.
type Credentials struct {
	Enabled   bool
	AppID     string
	AppSecret string
}

func (c Credentials) usable() bool {
	return c.Enabled && c.AppID != "" && c.AppSecret != ""
}

// Message is a group message that mentions the bot. QQ deduplicates replies
// by sequence number, so every reply to the same message takes the next one.
type Message struct {
	ID    string
	Group string
	User  string
	Text  string
	seq   atomic.Int32
}

func groupPath(groupID string) string {
	return "/v2/groups/" + url.PathEscape(groupID)
}

// APIError is an OpenAPI call QQ rejected. Code is QQ's business error code,
// for example 40034105 when the bot may not send proactive messages.
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("QQ API HTTP %d code %d: %s", e.Status, e.Code, e.Message)
}

// Client is the bot's connection to QQ: the gateway session in Run and the
// OpenAPI calls that answer messages and send notices.
type Client struct {
	credentials func() Credentials
	handler     func(*Message)
	http        *http.Client
	name        atomic.Pointer[string]

	tokenMu     sync.Mutex
	tokenFor    Credentials
	token       string
	tokenExpiry time.Time

	// Only the gateway loop touches the replay window.
	seen   map[string]struct{}
	recent [1024]string
	next   int
}

// New returns a client that reads the credentials before every connection and
// API call, so settings changes apply without a restart.
func New(credentials func() Credentials, handler func(*Message)) *Client {
	return &Client{
		credentials: credentials,
		handler:     handler,
		http: &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		seen: make(map[string]struct{}),
	}
}

// Name is the bot's QQ display name, known once the gateway is ready.
func (c *Client) Name() string {
	if name := c.name.Load(); name != nil {
		return *name
	}
	return ""
}

func (c *Client) call(ctx context.Context, method, endpoint, appID, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := common.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "QQBot "+token)
		req.Header.Set("X-Union-Appid", appID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("QQ API request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("QQ API response unreadable: %w", err)
	}
	var status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = common.Unmarshal(raw, &status)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || status.Code != 0 {
		return &APIError{Status: resp.StatusCode, Code: status.Code, Message: status.Message}
	}
	if out == nil {
		return nil
	}
	return common.Unmarshal(raw, out)
}

// accessToken returns a cached app access token, fetching a new one shortly
// before the old one expires or when the credentials change.
func (c *Client) accessToken(ctx context.Context, credentials Credentials) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.tokenFor == credentials && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}
	var result struct {
		Token string      `json:"access_token"`
		TTL   json.Number `json:"expires_in"`
	}
	request := map[string]string{"appId": credentials.AppID, "clientSecret": credentials.AppSecret}
	if err := c.call(ctx, http.MethodPost, tokenURL, "", "", request, &result); err != nil {
		return "", err
	}
	ttl, err := result.TTL.Int64()
	if err != nil || ttl < 1 || result.Token == "" {
		return "", errors.New("invalid QQ access token response")
	}
	c.tokenFor, c.token = credentials, result.Token
	c.tokenExpiry = time.Now().Add(time.Duration(ttl-min(ttl/2, 60)) * time.Second)
	return c.token, nil
}

// api calls the OpenAPI as the bot, retrying once with a fresh token when QQ
// no longer accepts the cached one.
func (c *Client) api(ctx context.Context, method, path string, in, out any) error {
	credentials := c.credentials()
	if !credentials.usable() {
		return errors.New("QQ bot is disabled or not configured")
	}
	for attempt := range 2 {
		token, err := c.accessToken(ctx, credentials)
		if err != nil {
			return err
		}
		err = c.call(ctx, method, apiBase+path, credentials.AppID, token, in, out)
		var apiErr *APIError
		if attempt == 1 || !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
			return err
		}
		c.tokenMu.Lock()
		if c.token == token {
			c.tokenExpiry = time.Time{}
		}
		c.tokenMu.Unlock()
	}
	return nil
}

// Reply answers a message with text.
func (c *Client) Reply(ctx context.Context, m *Message, text string) error {
	return c.api(ctx, http.MethodPost, groupPath(m.Group)+"/messages", map[string]any{
		"msg_type": 0,
		"content":  text,
		"msg_id":   m.ID,
		"msg_seq":  m.seq.Add(1),
	}, nil)
}

// ReplyImage answers a message with a PNG and an optional caption, sent
// together as one rich media message.
func (c *Client) ReplyImage(ctx context.Context, m *Message, caption string, png []byte) error {
	var file struct {
		Info string `json:"file_info"`
	}
	err := c.api(ctx, http.MethodPost, groupPath(m.Group)+"/files", map[string]any{
		"file_type":    1,
		"file_data":    base64.StdEncoding.EncodeToString(png),
		"srv_send_msg": false,
	}, &file)
	if err != nil {
		return err
	}
	if file.Info == "" {
		return errors.New("QQ image upload returned no file_info")
	}
	payload := map[string]any{
		"msg_type": 7,
		"media":    map[string]string{"file_info": file.Info},
		"msg_id":   m.ID,
		"msg_seq":  m.seq.Add(1),
	}
	if caption != "" {
		payload["content"] = caption
	}
	return c.api(ctx, http.MethodPost, groupPath(m.Group)+"/messages", payload, nil)
}

// SendGroup sends a proactive group message. QQ only accepts it once the bot
// has been granted proactive messaging; otherwise it fails with code 40034105.
func (c *Client) SendGroup(ctx context.Context, groupID, text string) error {
	return c.api(ctx, http.MethodPost, groupPath(groupID)+"/messages", map[string]any{"msg_type": 0, "content": text}, nil)
}
