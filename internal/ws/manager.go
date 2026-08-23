package ws

import (
	"sync"

	"github.com/gofiber/websocket/v2"
	"github.com/rs/zerolog"
)

// conn pairs a websocket connection with its own write mutex — gorilla-
// style websocket connections (and this fasthttp/websocket-based one)
// are not safe for concurrent writes from multiple goroutines, and a
// single user may have >1 device/tab connected, each writing
// independently as events arrive.
type conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// Manager is the in-memory connection registry: user id -> set of live
// connections. This is intentionally the entire "pub/sub" system at
// single-instance scale — see the scaling note in events.go for what
// changes once you run more than one backend process.
type Manager struct {
	mu    sync.RWMutex
	conns map[string]map[*conn]struct{}
	log   zerolog.Logger
}

func NewManager(log zerolog.Logger) *Manager {
	return &Manager{
		conns: make(map[string]map[*conn]struct{}),
		log:   log,
	}
}

// Register adds a connection for a user. Safe to call multiple times per
// user (multiple devices, or a reconnect racing a slow disconnect).
func (m *Manager) Register(userID string, ws *websocket.Conn) *conn {
	c := &conn{ws: ws}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conns[userID] == nil {
		m.conns[userID] = make(map[*conn]struct{})
	}
	m.conns[userID][c] = struct{}{}
	m.log.Info().Str("user_id", userID).Int("connections", len(m.conns[userID])).Msg("ws connected")
	return c
}

// Unregister removes a specific connection. Called from the handler's
// read-loop defer, so it always fires on disconnect/reconnect regardless
// of which side closed the socket.
func (m *Manager) Unregister(userID string, c *conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if set, ok := m.conns[userID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(m.conns, userID)
		}
	}
	m.log.Info().Str("user_id", userID).Msg("ws disconnected")
}

// SendToUser delivers an event to every connection currently open for
// that user id (0, 1, or many). Missing/offline users are a no-op by
// design — there's no outbox/replay queue at this scale; a client that
// reconnects simply re-fetches current state via the REST API (e.g.
// GET /service-requests/mine) rather than replaying missed events. That
// re-fetch-on-reconnect pattern is what makes this "reconnect-friendly"
// without needing message persistence.
func (m *Manager) SendToUser(userID string, evt Event) {
	m.mu.RLock()
	set, ok := m.conns[userID]
	if !ok {
		m.mu.RUnlock()
		m.log.Debug().Str("user_id", userID).Str("event", string(evt.Type)).Msg("ws: user offline — event dropped (client should re-fetch via REST)")
		return
	}
	// Copy the connection list out before writing, so a slow write
	// doesn't hold the registry lock and block unrelated users.
	targets := make([]*conn, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	m.mu.RUnlock()

	for _, c := range targets {
		c.mu.Lock()
		err := c.ws.WriteJSON(evt)
		c.mu.Unlock()
		if err != nil {
			m.log.Warn().Str("user_id", userID).Err(err).Msg("ws write failed, dropping connection")
			m.Unregister(userID, c)
			_ = c.ws.Close()
		}
	}
}

// SendToUsers is a small convenience for broadcasting the same event to
// several recipients (e.g. every nearby garage owner for a new request).
func (m *Manager) SendToUsers(userIDs []string, evt Event) {
	for _, id := range userIDs {
		m.SendToUser(id, evt)
	}
}
