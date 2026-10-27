package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	// How much a client may fall behind before updates are dropped for it.
	sendBuffer = 16
	// A dead TCP connection looks identical to an idle one. Ping to find out.
	pingEvery = 30 * time.Second
	// How long one frame may take to write before the client is hopeless.
	writeTimeout = 10 * time.Second
)

func (s *Server) positionsSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Websockets are not subject to same-origin policy, so without this
		// any page on the internet could open one against this API.
		OriginPatterns: s.cfg.OriginHosts(),
	})
	if err != nil {
		s.logger.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.CloseNow()

	// Nothing is expected from the client. CloseRead reads (and discards)
	// in the background so close frames and pings are handled, and cancels
	// ctx as soon as the client goes away, which ends the loop below.
	ctx := conn.CloseRead(r.Context())

	c := &wsClient{send: make(chan wsMessage, sendBuffer)}

	// Register before the snapshot, so an update that lands between the two
	// waits in c.send and is written after the snapshot. The other order
	// loses it: the snapshot is already old and the update was sent to
	// everyone else.
	s.hub.add(c)
	defer s.hub.remove(c)

	snap, err := s.snapshot(ctx)
	if err != nil {
		s.logger.Error("ws snapshot", "err", err)
		conn.Close(websocket.StatusInternalError, "could not load trucks")
		return
	}

	if err := writeJSONFrame(ctx, conn, snap); err != nil {
		return
	}
	s.pump(ctx, conn, c)
}

// driverSocket is GET /ws/driver (drivers): the driver's own thread, live.
// Each message dispatch sends them (or they send from another phone) comes
// as {"type":"message","message":{…}}, so the chat shows it at once. There's
// no snapshot: the app loads the thread with GET /auth/me/messages.
func (s *Server) driverSocket(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.cfg.OriginHosts(),
	})
	if err != nil {
		s.logger.Warn("driver ws upgrade failed", "err", err)
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	c := &wsClient{send: make(chan wsMessage, sendBuffer)}
	s.hub.addDriver(p.ID, c)
	defer s.hub.removeDriver(p.ID, c)

	if err := writeJSONFrame(ctx, conn, wsMessage{Type: "hello"}); err != nil {
		return
	}
	s.pump(ctx, conn, c)
}

// pump writes c's frames until the client goes or the hub drops it, and
// pings so a dead connection is noticed.
func (s *Server) pump(ctx context.Context, conn *websocket.Conn, c *wsClient) {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-c.send:
			if !ok {
				return
			}
			if err := writeJSONFrame(ctx, conn, m); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func writeJSONFrame(ctx context.Context, conn *websocket.Conn, m wsMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// snapshot is what a client gets on connect: every truck as it stands.
func (s *Server) snapshot(ctx context.Context) (wsMessage, error) {
	trucks, err := s.queries.ListTrucks(ctx)
	if err != nil {
		return wsMessage{}, err
	}
	out := make([]wsTruck, 0, len(trucks))
	for _, t := range trucks {
		out = append(out, wsTruck{
			ID:         t.ID.String(),
			Lng:        t.Lng,
			Lat:        t.Lat,
			LastSeenAt: t.LastSeenAt,
			Status:     t.Status,
		})
	}
	return wsMessage{Type: "snapshot", Trucks: out}, nil
}
