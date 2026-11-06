// Package onebot implements the OneBot 11 reverse WebSocket transport: NapCat
// dials in, sends events and answers actions over the same connection. It owns
// transport concerns only; commands live in the parent package.
package onebot

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gorilla/websocket"
)

const (
	maxPayload    = 5 << 20
	maxPending    = 64
	actionTimeout = 60 * time.Second
	verifyTimeout = 15 * time.Second
	writeTimeout  = 30 * time.Second
)

// Credentials is the peer identity the server currently accepts.
type Credentials struct {
	Enabled bool
	SelfID  string
	Secret  string
}

func (c Credentials) usable() bool {
	return c.Enabled && c.SelfID != "" && c.Secret != ""
}

// Segment is one OneBot message segment.
type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Text and Image build the outbound segments the bot sends.
func Text(text string) Segment {
	return Segment{Type: "text", Data: map[string]any{"text": text}}
}

func Image(file string) Segment {
	return Segment{Type: "image", Data: map[string]any{"file": file}}
}

// Event is an inbound OneBot event. IDs stay json.Number because they are only
// compared and echoed back, and OneBot implementations differ on quoting them.
type Event struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	UserID      json.Number     `json:"user_id"`
	GroupID     json.Number     `json:"group_id"`
	SelfID      json.Number     `json:"self_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
}

// EventHandler receives message events from the verified peer.
type EventHandler func(Event, *Peer)

// Peer is an authenticated NapCat connection.
type Peer struct {
	server *Server
	conn   *websocket.Conn
	write  sync.Mutex
}

// Send delivers message to the event's private or group destination. A failed
// send is not retried, because NapCat may already have applied the action.
func (p *Peer) Send(ctx context.Context, event Event, message []Segment) error {
	params := map[string]any{"message": message}
	action := "send_private_msg"
	switch event.MessageType {
	case "private":
		params["user_id"] = event.UserID
	case "group":
		action = "send_group_msg"
		params["group_id"] = event.GroupID
	default:
		return errors.New("unsupported OneBot message destination")
	}
	_, err := p.call(ctx, action, params)
	return err
}

func (p *Peer) call(ctx context.Context, action string, params map[string]any) (json.RawMessage, error) {
	s := p.server
	s.mu.Lock()
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		return nil, errors.New("OneBot action capacity reached")
	}
	echo := fmt.Sprintf("newapi-%d", s.seq.Add(1))
	result := make(chan actionResult, 1)
	s.pending[echo] = pendingAction{peer: p, result: result}
	s.mu.Unlock()
	defer s.removePending(echo)

	payload, err := common.Marshal(map[string]any{"action": action, "params": params, "echo": echo})
	if err != nil {
		return nil, err
	}
	p.write.Lock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err = p.conn.WriteMessage(websocket.TextMessage, payload)
	p.write.Unlock()
	if err != nil {
		return nil, errors.New("OneBot send failed")
	}
	timer := time.NewTimer(actionTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("OneBot action timed out; outcome unknown")
	case response := <-result:
		return response.data, response.err
	}
}

type pendingAction struct {
	peer   *Peer
	result chan actionResult
}

type actionResult struct {
	data json.RawMessage
	err  error
}

// Server is a single-peer OneBot reverse WebSocket endpoint. A second
// connection is rejected until the first one closes.
type Server struct {
	credentials func() Credentials
	handler     EventHandler
	upgrader    websocket.Upgrader
	peer        atomic.Pointer[Peer]
	seq         atomic.Uint64
	mu          sync.Mutex
	pending     map[string]pendingAction
}

// New returns a server that reads the accepted credentials on every handshake,
// so settings changes apply without a restart.
func New(credentials func() Credentials, handler EventHandler) *Server {
	return &Server{
		credentials: credentials,
		handler:     handler,
		// The peer is a bot client rather than a browser, and it authenticates
		// with a bearer secret that a cross-site page cannot attach.
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		pending:  make(map[string]pendingAction),
	}
}

// secretMatches compares digests so the comparison does not depend on, or
// reveal, the length of the configured secret.
func secretMatches(header, secret string) bool {
	presented, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	got, want := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// Peer returns the connected peer, or nil when there is none.
func (s *Server) Peer() *Peer {
	return s.peer.Load()
}

// ServeHTTP authenticates and upgrades a NapCat connection.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	credentials := s.credentials()
	if !credentials.usable() {
		http.NotFound(w, r)
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !secretMatches(auth[0], credentials.Secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-Self-ID") != credentials.SelfID {
		http.Error(w, "invalid self id", http.StatusForbidden)
		return
	}
	if s.peer.Load() != nil {
		http.Error(w, "peer already connected", http.StatusConflict)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.serve(conn, credentials)
}

func (s *Server) serve(conn *websocket.Conn, accepted Credentials) {
	defer conn.Close()
	p := &Peer{server: s, conn: conn}
	if !s.peer.CompareAndSwap(nil, p) {
		return
	}
	defer func() {
		s.peer.CompareAndSwap(p, nil)
		s.failPending(p)
	}()
	conn.SetReadLimit(maxPayload)

	// The read loop must already run for the verification answer to arrive;
	// events are dropped until it has passed.
	var verified atomic.Bool
	go func() {
		if err := p.verify(accepted.SelfID); err != nil {
			common.SysError(common.LogText("OneBot peer verification failed: %s", err.Error()))
			_ = conn.Close()
			return
		}
		verified.Store(true)
		common.SysLog(common.LogText("OneBot peer connected: %s", accepted.SelfID))
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// Rotating the secret or disabling the bot must end a session that was
		// authorized under the previous settings.
		if s.credentials() != accepted {
			return
		}
		var frame struct {
			Echo    *string         `json:"echo"`
			Status  string          `json:"status"`
			RetCode json.Number     `json:"retcode"`
			Data    json.RawMessage `json:"data"`
		}
		if common.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame.Echo != nil {
			s.resolve(*frame.Echo, frame.Status == "ok" && frame.RetCode == "0", frame.Data)
			continue
		}
		var event Event
		if !verified.Load() || common.Unmarshal(raw, &event) != nil || event.PostType != "message" {
			continue
		}
		go s.handler(event, p)
	}
}

func (p *Peer) verify(selfID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	raw, err := p.call(ctx, "get_login_info", map[string]any{})
	if err != nil {
		return err
	}
	var login struct {
		UserID json.Number `json:"user_id"`
	}
	if err := common.Unmarshal(raw, &login); err != nil {
		return err
	}
	if login.UserID.String() != selfID {
		return errors.New("login account does not match self ID")
	}
	return nil
}

func (s *Server) resolve(echo string, ok bool, data json.RawMessage) {
	s.mu.Lock()
	action, found := s.pending[echo]
	delete(s.pending, echo)
	s.mu.Unlock()
	if !found {
		return
	}
	if !ok {
		action.result <- actionResult{err: errors.New("OneBot action failed")}
		return
	}
	action.result <- actionResult{data: data}
}

func (s *Server) removePending(echo string) {
	s.mu.Lock()
	delete(s.pending, echo)
	s.mu.Unlock()
}

func (s *Server) failPending(p *Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for echo, action := range s.pending {
		if action.peer == p {
			delete(s.pending, echo)
			action.result <- actionResult{err: errors.New("OneBot disconnected")}
		}
	}
}
