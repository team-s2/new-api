package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// pkceOAuthSession binds a pending authorization to the browser that started it.
// The store replaces OwnerSession with its hash before retaining the session.
type pkceOAuthSession struct {
	OwnerSession  string
	State         string
	CodeVerifier  string
	CodeChallenge string
	CreatedAt     time.Time
}

type pkceOAuthSessionStore struct {
	redisKeyPrefix string
	ttl            time.Duration
	mu             sync.Mutex
	sessions       map[string]*pkceOAuthSession
}

const pkceOAuthStorageTimeout = 5 * time.Second

const pkceOAuthConsumeScript = `
local owner = redis.call('HGET', KEYS[1], 'owner_session')
if not owner or owner ~= ARGV[1] then
  return nil
end
local payload = redis.call('HGET', KEYS[1], 'payload')
redis.call('DEL', KEYS[1])
return payload
`

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *pkceOAuthSessionStore) store(sessionID string, session *pkceOAuthSession) error {
	if sessionID == "" || session == nil || session.OwnerSession == "" {
		return errors.New("oauth session: missing browser binding")
	}
	stored := *session
	ownerHash := sha256.Sum256([]byte(session.OwnerSession))
	stored.OwnerSession = hex.EncodeToString(ownerHash[:])
	remaining := time.Until(stored.CreatedAt.Add(s.ttl))
	if remaining <= 0 {
		return errors.New("oauth session: already expired")
	}
	if common.RedisEnabled {
		// Redis is authoritative across workers. Never retain a local fallback
		// that could accept a session another worker has already consumed.
		if common.RDB == nil {
			return errors.New("oauth session: shared storage unavailable")
		}
		payload, err := common.Marshal(&stored)
		if err != nil {
			return errors.New("oauth session: failed to encode session")
		}
		ctx, cancel := context.WithTimeout(context.Background(), pkceOAuthStorageTimeout)
		defer cancel()
		key := s.redisKeyPrefix + sessionID
		pipe := common.RDB.TxPipeline()
		pipe.HSet(ctx, key, "owner_session", stored.OwnerSession, "payload", string(payload))
		pipe.Expire(ctx, key, remaining)
		if _, err := pipe.Exec(ctx); err != nil {
			return errors.New("oauth session: shared storage unavailable")
		}
		return nil
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*pkceOAuthSession)
	}
	s.sessions[sessionID] = &stored
	s.mu.Unlock()
	time.AfterFunc(remaining, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sessions[sessionID] == &stored {
			delete(s.sessions, sessionID)
		}
	})
	return nil
}

func (s *pkceOAuthSessionStore) consume(sessionID, ownerSession string) (*pkceOAuthSession, bool) {
	if sessionID == "" || ownerSession == "" {
		return nil, false
	}
	ownerHash := sha256.Sum256([]byte(ownerSession))
	ownerDigest := hex.EncodeToString(ownerHash[:])
	var session *pkceOAuthSession
	if common.RedisEnabled {
		if common.RDB == nil {
			return nil, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), pkceOAuthStorageTimeout)
		defer cancel()
		result, err := common.RDB.Eval(ctx, pkceOAuthConsumeScript, []string{s.redisKeyPrefix + sessionID}, ownerDigest).Result()
		if err != nil {
			return nil, false
		}
		payload, ok := result.(string)
		if !ok || payload == "" || common.Unmarshal([]byte(payload), &session) != nil {
			return nil, false
		}
	} else {
		s.mu.Lock()
		session = s.sessions[sessionID]
		if session != nil && constantTimeEqual(session.OwnerSession, ownerDigest) {
			delete(s.sessions, sessionID)
		} else {
			session = nil
		}
		s.mu.Unlock()
	}
	if session == nil || !constantTimeEqual(session.OwnerSession, ownerDigest) || !time.Now().Before(session.CreatedAt.Add(s.ttl)) {
		return nil, false
	}
	return session, true
}
