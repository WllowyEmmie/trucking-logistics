package api

import (
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

type wsMessage struct {
	Type string `json:"type"` // "snapshot" | "position" | "changed" | "message"
	// Set on snapshot: every truck as the client should start out.
	Trucks []wsTruck `json:"trucks,omitempty"`
	// Set on position: the one truck that moved.
	Truck *wsTruck `json:"truck,omitempty"`
	// Set on changed: what to refetch ("deliveries", "trucks", "drivers",
	// "proposals", "config"), and the delivery when one was changed.
	Topics     []string `json:"topics,omitempty"`
	DeliveryID string   `json:"delivery_id,omitempty"`
	// Set on message (driver sockets only): the message just written in
	// that driver's thread, by dispatch or by the driver on another phone.
	Message any `json:"message,omitempty"`
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
	// Drivers' phones, by driver. They never get broadcasts: only what
	// toDriver sends them (their own thread).
	drivers map[uuid.UUID]map[*wsClient]struct{}
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
		drivers: make(map[uuid.UUID]map[*wsClient]struct{}),
	}
}

// count is how many clients are connected (for /metrics).
func (h *hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
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

func (h *hub) addDriver(id uuid.UUID, c *wsClient) {
	h.mu.Lock()
	set := h.drivers[id]
	if set == nil {
		set = make(map[*wsClient]struct{})
		h.drivers[id] = set
	}
	set[c] = struct{}{}
	h.mu.Unlock()
	h.logger.Info("driver ws connected", "driver", id)
}

func (h *hub) removeDriver(id uuid.UUID, c *wsClient) {
	h.mu.Lock()
	if set := h.drivers[id]; set != nil {
		if _, ok := set[c]; ok {
			delete(set, c)
			close(c.send)
		}
		if len(set) == 0 {
			delete(h.drivers, id)
		}
	}
	h.mu.Unlock()
	h.logger.Info("driver ws disconnected", "driver", id)
}

// toDriver sends to every phone that driver has open. Like broadcast, it
// never blocks: a phone that's behind misses the frame and catches up on
// its next poll.
func (h *hub) toDriver(id uuid.UUID, m wsMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.drivers[id] {
		select {
		case c.send <- m:
		default:
			h.logger.Warn("driver ws is behind, dropping message", "driver", id)
		}
	}
}
