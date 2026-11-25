package official

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gorilla/websocket"
)

// Gateway opcodes and the intent this bot subscribes to.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	// intentGroupMessages delivers group messages; QQ bundles private messages
	// into the same intent, and the bot ignores them.
	intentGroupMessages = 1 << 25

	maxFrame     = 4 << 20
	helloTimeout = 20 * time.Second
	writeTimeout = 10 * time.Second
	idleRecheck  = 10 * time.Second
	maxBackoff   = time.Minute
)

var errCredentialsChanged = errors.New("QQ bot credentials changed")

type frame struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
	Seq  *int64          `json:"s"`
	Type string          `json:"t"`
}

// session is what a reconnect needs to resume instead of identifying again.
type session struct {
	credentials Credentials
	id          string
	seq         *int64
}

// Run keeps one gateway session while the bot is enabled and configured. QQ
// allows a single session per bot, so only one node may call it. Drops are
// resumed; a credentials change starts a new session.
func (c *Client) Run(ctx context.Context) {
	var s session
	backoff := time.Second
	for ctx.Err() == nil {
		credentials := c.credentials()
		if !credentials.usable() {
			s = session{}
			sleep(ctx, idleRecheck)
			continue
		}
		if s.credentials != credentials {
			s = session{credentials: credentials}
		}
		ready, err := c.connect(ctx, &s)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errCredentialsChanged) {
			backoff = time.Second
			continue
		}
		if err != nil {
			common.SysError(common.LogText("QQ gateway unavailable: %s", err.Error()))
		}
		if ready {
			backoff = time.Second
		}
		sleep(ctx, backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// connect runs one gateway connection until it drops. It reports whether the
// session became ready, which resets the reconnect backoff.
func (c *Client) connect(ctx context.Context, s *session) (bool, error) {
	var gateway struct {
		URL string `json:"url"`
	}
	if err := c.api(ctx, http.MethodGet, "/gateway", nil, &gateway); err != nil {
		return false, err
	}
	if !strings.HasPrefix(gateway.URL, "wss://") {
		return false, errors.New("QQ returned an invalid gateway URL")
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, gateway.URL, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetReadLimit(maxFrame)

	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	var hello frame
	if err := readFrame(conn, &hello); err != nil {
		return false, err
	}
	var timing struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if hello.Op != opHello || common.Unmarshal(hello.Data, &timing) != nil || timing.Interval <= 0 {
		return false, errors.New("invalid QQ gateway hello")
	}
	_ = conn.SetReadDeadline(time.Time{})

	token, err := c.accessToken(ctx, s.credentials)
	if err != nil {
		return false, err
	}
	auth := map[string]any{"token": "QQBot " + token}
	if s.id != "" && s.seq != nil {
		auth["session_id"], auth["seq"] = s.id, *s.seq
		err = writeFrame(conn, opResume, auth)
	} else {
		auth["intents"] = intentGroupMessages
		auth["shard"] = []int{0, 1}
		err = writeFrame(conn, opIdentify, auth)
	}
	if err != nil {
		return false, err
	}

	// Only this loop writes to the connection; the reader just hands frames over.
	frames := make(chan frame)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			var f frame
			if err := readFrame(conn, &f); err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()

	heartbeat := time.NewTicker(time.Duration(timing.Interval) * time.Millisecond)
	defer heartbeat.Stop()
	ready, awaitingAck := false, false
	for {
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case err := <-readErr:
			// These close codes mean the session cannot be resumed.
			if websocket.IsCloseError(err, 4006, 4007, 4009) {
				*s = session{credentials: s.credentials}
			}
			return ready, err
		case <-heartbeat.C:
			// Disabling the bot or rotating the secret ends a session that was
			// opened under the previous settings.
			if c.credentials() != s.credentials {
				return ready, errCredentialsChanged
			}
			if awaitingAck {
				return ready, errors.New("QQ gateway heartbeat timed out")
			}
			if err := writeFrame(conn, opHeartbeat, s.seq); err != nil {
				return ready, err
			}
			awaitingAck = true
		case f := <-frames:
			switch f.Op {
			case opHeartbeatAck:
				awaitingAck = false
			case opHeartbeat:
				if err := writeFrame(conn, opHeartbeat, s.seq); err != nil {
					return ready, err
				}
			case opReconnect:
				return ready, nil
			case opInvalidSession:
				*s = session{credentials: s.credentials}
				return ready, errors.New("QQ gateway rejected the session")
			case opDispatch:
				if f.Seq != nil {
					s.seq = f.Seq
				}
				switch f.Type {
				case "READY":
					var r struct {
						SessionID string `json:"session_id"`
						User      struct {
							Username string `json:"username"`
						} `json:"user"`
					}
					if common.Unmarshal(f.Data, &r) != nil || r.SessionID == "" {
						return ready, errors.New("invalid QQ gateway ready event")
					}
					s.id = r.SessionID
					c.name.Store(&r.User.Username)
					ready = true
					common.SysLog(common.LogText("QQ gateway connected as %s", r.User.Username))
				case "RESUMED":
					ready = true
				case "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE":
					if ready {
						c.dispatch(f.Type, f.Data)
					}
				}
			}
		}
	}
}

func readFrame(conn *websocket.Conn, f *frame) error {
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	return common.Unmarshal(raw, f)
}

func writeFrame(conn *websocket.Conn, op int, data any) error {
	raw, err := common.Marshal(map[string]any{"op": op, "d": data})
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, raw)
}

// dispatch hands a message to the handler once, even when a resumed session
// or both group events deliver it again. A bot allowed to read every group
// message receives GROUP_MESSAGE_CREATE instead of GROUP_AT_MESSAGE_CREATE, so
// only messages that mention the bot count there, with the mention removed.
func (c *Client) dispatch(event string, raw json.RawMessage) {
	var payload struct {
		ID          string `json:"id"`
		Content     string `json:"content"`
		GroupOpenID string `json:"group_openid"`
		Author      struct {
			MemberOpenID string `json:"member_openid"`
			Bot          bool   `json:"bot"`
		} `json:"author"`
		Mentions []struct {
			MemberOpenID string `json:"member_openid"`
			IsYou        bool   `json:"is_you"`
		} `json:"mentions"`
	}
	if common.Unmarshal(raw, &payload) != nil || payload.ID == "" || payload.Author.Bot {
		return
	}
	m := &Message{ID: payload.ID, Group: payload.GroupOpenID, User: payload.Author.MemberOpenID, Text: payload.Content}
	if m.Group == "" || m.User == "" {
		return
	}
	if event == "GROUP_MESSAGE_CREATE" {
		mentioned := false
		for _, mention := range payload.Mentions {
			if mention.IsYou {
				mentioned = true
				m.Text = strings.NewReplacer("<@"+mention.MemberOpenID+">", "", "<@!"+mention.MemberOpenID+">", "").Replace(m.Text)
				break
			}
		}
		if !mentioned {
			return
		}
	}
	m.Text = strings.TrimSpace(m.Text)
	if _, duplicate := c.seen[m.ID]; duplicate {
		return
	}
	delete(c.seen, c.recent[c.next])
	c.recent[c.next] = m.ID
	c.next = (c.next + 1) % len(c.recent)
	c.seen[m.ID] = struct{}{}
	go c.handler(m)
}
