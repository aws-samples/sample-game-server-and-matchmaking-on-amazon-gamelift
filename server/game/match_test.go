package game

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type denyAll struct{}

func (denyAll) Validate(string) error { return errors.New("denied") }

func startMatch(t *testing.T, cfg Config) (string, <-chan Result) {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg)
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	done := make(chan Result, 1)
	go func() { res, _ := srv.Run(ctx); done <- res }()
	return "ws://" + addr.String(), done
}

func dial(t *testing.T, url string, header http.Header) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// join sends the join message and returns the first reply.
func join(t *testing.T, c *websocket.Conn, playerSessionID string) map[string]any {
	t.Helper()
	if err := c.WriteJSON(map[string]string{"type": "join", "playerSessionId": playerSessionID}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var reply map[string]any
	if err := c.ReadJSON(&reply); err != nil {
		t.Fatalf("no reply to join: %v", err)
	}
	return reply
}

func waitResult(t *testing.T, done <-chan Result) Result {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("match did not end")
		return Result{}
	}
}

func TestMatchPlaysToTheEnd(t *testing.T) {
	url, done := startMatch(t, Config{ScoreToWin: 1, EndGrace: 100 * time.Millisecond})
	a, b := dial(t, url, nil), dial(t, url, nil)
	if r := join(t, a, "psess-a"); r["type"] != "joined" || r["seat"] != 0.0 {
		t.Fatalf("a: %v", r)
	}
	if r := join(t, b, "psess-b"); r["type"] != "joined" || r["seat"] != 1.0 {
		t.Fatalf("b: %v", r)
	}
	// Seat 0 parks its paddle at the top, so the first serve (toward seat 0) is a goal for seat 1.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				a.WriteJSON(map[string]any{"type": "move", "dir": -1})
			}
		}
	}()
	res := waitResult(t, done)
	if res.Winner != 1 || res.Reason != "completed" || len(res.PlayerSessions) != 2 {
		t.Fatalf("result = %+v", res)
	}
}

func TestStateReachesPlayers(t *testing.T) {
	url, _ := startMatch(t, Config{})
	a, b := dial(t, url, nil), dial(t, url, nil)
	join(t, a, "a")
	join(t, b, "b")
	for i := 0; i < 20; i++ {
		var msg map[string]any
		if err := a.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg["type"] == "state" && msg["phase"] != string(Waiting) {
			return
		}
	}
	t.Fatal("no state with the match started")
}

func TestLeavingForfeits(t *testing.T) {
	url, done := startMatch(t, Config{EndGrace: 100 * time.Millisecond})
	a, b := dial(t, url, nil), dial(t, url, nil)
	join(t, a, "a")
	join(t, b, "b")
	b.Close()
	if res := waitResult(t, done); res.Winner != 0 || res.Reason != "forfeit" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRefusedJoins(t *testing.T) {
	url, _ := startMatch(t, Config{Validator: denyAll{}})
	if r := join(t, dial(t, url, nil), "x"); r["type"] != "rejected" {
		t.Fatalf("validator refusal not applied: %v", r)
	}

	url, _ = startMatch(t, Config{})
	join(t, dial(t, url, nil), "same")
	if r := join(t, dial(t, url, nil), "same"); r["type"] != "rejected" {
		t.Fatalf("one player session took two seats: %v", r)
	}
	join(t, dial(t, url, nil), "other")
	if r := join(t, dial(t, url, nil), "third"); r["type"] != "rejected" {
		t.Fatalf("a third player was seated: %v", r)
	}
}

func TestOriginCheck(t *testing.T) {
	url, _ := startMatch(t, Config{AllowedOrigins: []string{"http://127.0.0.1:8080"}})
	if _, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://example.com"}}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a page from another site connected (err %v)", err)
	}
	dial(t, url, http.Header{"Origin": {"http://127.0.0.1:8080"}}) // the listed page connects
	dial(t, url, nil)                                              // a non-browser client connects
}

func TestBadFirstMessageIsDropped(t *testing.T) {
	url, _ := startMatch(t, Config{})
	c := dial(t, url, nil)
	c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 2*maxMessage)))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("server answered an oversized, non-JSON first message")
	}
}

func TestFirstMessageThatIsNotJoinIsDropped(t *testing.T) {
	url, _ := startMatch(t, Config{})
	c := dial(t, url, nil)
	c.WriteJSON(map[string]string{"type": "move", "playerSessionId": "psess-a"}) // small and well formed, but not a join
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("server answered a first message that was not a join")
	}
}

func TestNobodyJoins(t *testing.T) {
	_, done := startMatch(t, Config{JoinTimeout: 200 * time.Millisecond})
	if res := waitResult(t, done); res.Reason != "no players" {
		t.Fatalf("result = %+v", res)
	}
}

// With Humans = 1 the match starts as soon as one player joins, and the server plays the other seat.
func TestComputerPlaysTheMissingSeat(t *testing.T) {
	url, done := startMatch(t, Config{Humans: 1, ScoreToWin: 1, EndGrace: 100 * time.Millisecond})
	a := dial(t, url, nil)
	if r := join(t, a, "psess-a"); r["type"] != "joined" || r["seat"] != 0.0 {
		t.Fatalf("a: %v", r)
	}
	// Seat 0 parks its paddle at the top, so the first serve (toward seat 0) is a goal for the computer.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				a.WriteJSON(map[string]any{"type": "move", "dir": -1})
			}
		}
	}()
	res := waitResult(t, done)
	if res.Winner != 1 || res.Reason != "completed" || len(res.PlayerSessions) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// A connection that closes must not stay in the server's list, and its writer goroutine must stop. A match
// lasts minutes, so a leak per connection is small, but a sample should show the clean pattern.
func TestClosedConnectionIsForgotten(t *testing.T) {
	srv := New(Config{Addr: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go srv.Run(ctx)
	time.Sleep(100 * time.Millisecond) // the server's own goroutines have started
	before := runtime.NumGoroutine()

	c := dial(t, "ws://"+addr.String(), nil)
	if r := join(t, c, "psess-a"); r["type"] != "joined" {
		t.Fatalf("join: %v", r)
	}
	c.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.mu.Lock()
		tracked := len(srv.conns)
		srv.mu.Unlock()
		if tracked == 0 && runtime.NumGoroutine() <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the connection closed: %d tracked connections, %d goroutines (was %d)", tracked, runtime.NumGoroutine(), before)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The port refuses connections beyond the per-source and total limits, and gives the slots back when connections close.
func TestConnectionsPerSourceAreLimited(t *testing.T) {
	url, _ := startMatch(t, Config{MaxPerSource: 2, MaxConnections: 10})
	a, b := dial(t, url, nil), dial(t, url, nil) // both from 127.0.0.1: the two allowed slots
	_ = b
	// A third connection from the same address is accepted by the operating system and then closed by the server.
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		c.WriteJSON(map[string]string{"type": "join", "playerSessionId": "psess-c"})
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var reply map[string]any
		if c.ReadJSON(&reply) == nil {
			t.Fatalf("a third connection from one address was served: %v", reply)
		}
		c.Close()
	}
	// Freeing a slot lets a new connection in.
	a.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		d, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			d.WriteJSON(map[string]string{"type": "join", "playerSessionId": "psess-d"})
			d.SetReadDeadline(time.Now().Add(2 * time.Second))
			var reply map[string]any
			if d.ReadJSON(&reply) == nil && reply["type"] == "joined" {
				d.Close()
				return
			}
			d.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("a connection was still refused after a slot was freed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTotalConnectionsAreLimited(t *testing.T) {
	url, _ := startMatch(t, Config{MaxPerSource: 100, MaxConnections: 2})
	dial(t, url, nil)
	dial(t, url, nil)
	// The total limit queues further connections: the handshake does not finish while both slots are taken.
	d := websocket.Dialer{HandshakeTimeout: 500 * time.Millisecond}
	if c, _, err := d.Dial(url, nil); err == nil {
		c.Close()
		t.Fatal("a connection beyond the total limit was served")
	}
}

// slowValidator holds a join in the player session check until released, like a slow call to the service.
type slowValidator struct{ release chan struct{} }

func (v slowValidator) Validate(string) error { <-v.release; return nil }

// A join that is still being checked when the match ends must not leave a goroutine waiting for the match loop.
func TestValidateThatOutlivesRunDoesNotLeak(t *testing.T) {
	v := slowValidator{release: make(chan struct{})}
	cfg := Config{Addr: "127.0.0.1:0", Validator: v, JoinTimeout: 300 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := New(cfg)
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { srv.Run(context.Background()); close(done) }()
	c, _, err := websocket.DefaultDialer.Dial("ws://"+addr.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.WriteJSON(map[string]string{"type": "join", "playerSessionId": "p1"})
	<-done // Run ended while the check is still blocked
	close(v.release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<16)
		if !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "serveWS") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a connection goroutine is still waiting for the match loop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
