package api

import (
	"log/slog"
	"sync"
	"time"
)

type wsMessage struct {
	Type string `json:"type"` // "snapshot" | "position" | "changed"
	// Set on snapshot: every truck as the client should start out.
	Trucks []wsTruck `json:"trucks,omitempty"`
	// Set on position: the one truck that moved.
	Truck *wsTruck `json:"truck,omitempty"`
	// Set on changed: what to refetch ("deliveries", "trucks", "drivers",
	// "proposals", "config"), and the delivery when one was changed.
	Topics     []string `json:"topics,omitempty"`
	DeliveryID string   `json:"delivery_id,omitempty"`
}

type wsTruck struct {
	ID         string     `json:"id"`
	Lng        *float64   `json:"lng"`
	Lat        *float64   `json:"lat"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	Status     string     `json:"status,omitempty"`
}

// hub owns the set of connected clients. Nothing outside this file touches
// that map — every client registers, unregisters and receives through
// channels, so there is exactly one goroutine mutating it.
type hub struct {
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[*wsClient]struct{}
}

// wsClient is one open connection. send is buffered: a slow client fills it
// and gets dropped rather than blocking the broadcaster.
type wsClient struct {
	send chan wsMessage
}

func newHub(logger *slog.Logger) *hub {
	return &hub{
		logger:  logger,
		clients: make(map[*wsClient]struct{}),
	}
}

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	n := len(h.clients)
	h.mu.Unlock()
	h.logger.Info("ws client connected", "clients", n)
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	n := len(h.clients)
	h.mu.Unlock()
	h.logger.Info("ws client disconnected", "clients", n)
}

// broadcast never blocks. A client whose buffer is full is too far behind to
// be worth waiting for: drop the message for that client only.
func (h *hub) broadcast(m wsMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- m:
		default:
			h.logger.Warn("ws client is behind, dropping update")
		}
	}
}
