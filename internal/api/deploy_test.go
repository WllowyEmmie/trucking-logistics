package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A browser on another host opens the socket with a one-minute pass it got
// through its own sign-in. The pass works once; without one, nothing.
func TestSocketPass(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var pass struct {
		Ticket string `json:"ticket"`
	}
	if err := e.c.Get(ctx, "/ws/ticket", &pass); err != nil || len(pass.Ticket) != 64 {
		t.Fatalf("pass: %+v %v", pass, err)
	}
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	origin := http.Header{"Origin": {"http://localhost:3000"}}
	conn, _, err := websocket.Dial(ctx, wsURL+"?ticket="+pass.Ticket, &websocket.DialOptions{HTTPHeader: origin})
	if err != nil {
		t.Fatalf("with a pass: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	conn.CloseNow()
	if _, res, err := websocket.Dial(ctx, wsURL+"?ticket="+pass.Ticket, &websocket.DialOptions{HTTPHeader: origin}); err == nil || res == nil || res.StatusCode != 401 {
		t.Fatalf("the same pass twice: %v", err)
	}
	if got := e.raw(t, "GET", "/ws/ticket", "", "", ""); got != 401 {
		t.Fatalf("a pass without signing in: %d", got)
	}
}

// With AGENT_INTERNAL_URL set, /agent/* reaches the agent, cookie and all,
// and longer than the API's 15 s request timeout.
func TestAgentProxy(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "cookie": r.Header.Get("Cookie"), "body": string(b)})
	}))
	defer agent.Close()
	t.Setenv("AGENT_INTERNAL_URL", agent.URL)
	e := setup(t)

	req, _ := http.NewRequest("POST", e.srv.URL+"/agent/track/abc/ask", strings.NewReader(`{"question":"where"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "session=x")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got map[string]string
	json.NewDecoder(res.Body).Decode(&got)
	if res.StatusCode != 200 || got["path"] != "/track/abc/ask" || got["cookie"] != "session=x" || got["body"] != `{"question":"where"}` {
		t.Fatalf("%d %+v", res.StatusCode, got)
	}
}
