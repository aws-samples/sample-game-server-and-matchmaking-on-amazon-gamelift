package gamelift

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/amazon-gamelift/amazon-gamelift-servers-go-server-sdk/v5/model"
	"github.com/gorilla/websocket"

	"gameserver/game"
)

// fakePlatform records the calls the host makes and can be told to fail.
type fakePlatform struct {
	mu          sync.Mutex
	calls       []string
	activateErr error
	rejectToken string
	endings     int
}

func (f *fakePlatform) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakePlatform) ActivateGameSession() error { f.record("activate"); return f.activateErr }
func (f *fakePlatform) AcceptPlayerSession(id string) error {
	f.record("accept:" + id)
	if id == f.rejectToken {
		return errors.New("invalid player session")
	}
	return nil
}
func (f *fakePlatform) RemovePlayerSession(id string) error { f.record("remove:" + id); return nil }
func (f *fakePlatform) ProcessEnding() error {
	f.mu.Lock()
	f.endings++
	f.mu.Unlock()
	return nil
}

func (f *fakePlatform) has(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakePlatform) first() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[0]
}

func newHost(p Platform) *Host {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(p, logger, PongGame(game.Config{
		Addr:     "127.0.0.1:0",
		EndGrace: 100 * time.Millisecond,
		Logger:   logger,
	}))
}

func waitDone(t *testing.T, h *Host) {
	t.Helper()
	select {
	case <-h.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("host did not finish in time")
	}
}

// join connects like a game client and reports whether the server seated the player.
func join(t *testing.T, h *Host, playerSessionID string) (*websocket.Conn, bool) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+h.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.WriteJSON(map[string]string{"type": "join", "playerSessionId": playerSessionID})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var reply map[string]any
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatalf("no reply to join: %v", err)
	}
	return conn, reply["type"] == "joined"
}

// holdUp keeps a player's paddle moving up until stop is closed.
func holdUp(conn *websocket.Conn, stop <-chan struct{}) {
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				conn.WriteJSON(map[string]any{"type": "move", "dir": -1})
			}
		}
	}()
}

func TestFullLifecycle(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs-1", Properties: map[string]string{ScorePropertyKey: "1"}}); err != nil {
		t.Fatal(err)
	}
	if p.first() != "activate" {
		t.Fatalf("first platform call = %q; the socket must be bound and the session activated before players join", p.first())
	}
	a, okA := join(t, h, "psess-a")
	_, okB := join(t, h, "psess-b")
	if !okA || !okB {
		t.Fatal("players were refused")
	}
	stop := make(chan struct{})
	defer close(stop)
	holdUp(a, stop) // seat 0 misses the first serve, so seat 1 wins 1-0

	waitDone(t, h)
	for _, call := range []string{"accept:psess-a", "accept:psess-b", "remove:psess-a", "remove:psess-b"} {
		if !p.has(call) {
			t.Errorf("missing platform call %q (calls: %v)", call, p.calls)
		}
	}
	if p.endings != 1 {
		t.Errorf("ProcessEnding called %d times, want 1", p.endings)
	}
}

func TestPlatformRejectionRefusesPlayer(t *testing.T) {
	p := &fakePlatform{rejectToken: "forged"}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs-1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Terminate)
	if _, ok := join(t, h, "forged"); ok {
		t.Fatal("a player session the platform rejected was accepted")
	}
}

func TestInvalidScorePropertyEndsProcess(t *testing.T) {
	for _, value := range []string{"0", "21", "abc", "-1", "1.5"} {
		p := &fakePlatform{}
		h := newHost(p)
		err := h.StartSession(context.Background(), Session{ID: "gs", Properties: map[string]string{ScorePropertyKey: value}})
		if err == nil {
			t.Errorf("scoreToWin=%q accepted", value)
			continue
		}
		waitDone(t, h)
		if p.has("activate") {
			t.Errorf("scoreToWin=%q: session was activated despite the error", value)
		}
		if p.endings != 1 {
			t.Errorf("scoreToWin=%q: ProcessEnding called %d times", value, p.endings)
		}
	}
}

func TestActivateFailureEndsProcess(t *testing.T) {
	p := &fakePlatform{activateErr: errors.New("boom")}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err == nil {
		t.Fatal("expected an error")
	}
	waitDone(t, h)
	if p.endings != 1 {
		t.Fatalf("ProcessEnding called %d times", p.endings)
	}
}

func TestSecondSessionIsRefused(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Terminate)
	if err := h.StartSession(context.Background(), Session{ID: "two"}); err == nil {
		t.Fatal("a process accepted a second game session")
	}
}

func TestTerminateStopsRunningMatch(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := join(t, h, "psess-a"); !ok {
		t.Fatal("player was not seated")
	}
	h.Terminate()
	waitDone(t, h)
	if p.endings != 1 {
		t.Fatalf("ProcessEnding called %d times", p.endings)
	}
	if !p.has("remove:psess-a") {
		t.Fatal("terminating a running match did not remove the player's session")
	}
}

func TestTerminateWhenIdle(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if !h.Healthy() {
		t.Fatal("an idle process should report healthy")
	}
	h.Terminate()
	h.Terminate() // calling twice is harmless
	waitDone(t, h)
	if p.endings != 1 {
		t.Fatalf("ProcessEnding called %d times", p.endings)
	}
}

func TestHealthyWhileMatchRuns(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Terminate)
	time.Sleep(200 * time.Millisecond)
	if !h.Healthy() {
		t.Fatal("a running match should report healthy")
	}
}

// A match loop that stops ticking is a stuck process: the health check must say so, so the service replaces it.
func TestUnhealthyWhenMatchStopsTicking(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Terminate)
	time.Sleep(200 * time.Millisecond) // the loop has ticked
	if !h.Healthy() {
		t.Fatal("a ticking match should be healthy")
	}
	h.now = func() time.Time { return time.Now().Add(2 * healthStaleAfter) } // as if no tick came for a long time
	if h.Healthy() {
		t.Fatal("a match that stopped ticking reported healthy")
	}
}

func TestTLSLoadedAtSessionStart(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	calls := 0
	h.loadTLS = func() (*tls.Config, error) { calls++; return nil, errors.New("no certificate") }
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err == nil {
		t.Fatal("a session started without its TLS certificate")
	}
	waitDone(t, h)
	if calls != 1 || p.has("activate") || p.endings != 1 {
		t.Fatalf("calls=%d activated=%v endings=%d; want the certificate loaded once, no activation, one ProcessEnding", calls, p.has("activate"), p.endings)
	}
}

func TestHumansInMatchmakerData(t *testing.T) {
	one := `{"matchId":"m","teams":[{"name":"players","players":[{"playerId":"a"}]}]}`
	two := `{"matchId":"m","teams":[{"name":"players","players":[{"playerId":"a"},{"playerId":"b"}]}]}`
	for data, want := range map[string]int{one: 1, two: 2, "": 0, "not json": 0} {
		if got := humansIn(model.GameSession{MatchmakerData: data}); got != want {
			t.Errorf("humansIn(%q) = %d, want %d", data, got, want)
		}
	}
}

// toyGame is the smallest possible Game: it shows what the lifecycle needs from a game that isn't Pong. It has no
// network protocol; "players" are the IDs the test passes to join, and the match ends when finish is closed.
type toyGame struct {
	setup  Setup
	joined []string
	finish chan struct{}
}

func (g *toyGame) Listen() (netip.AddrPort, error) {
	return netip.MustParseAddrPort("127.0.0.1:4242"), nil
}

func (g *toyGame) Run(ctx context.Context) (Outcome, error) {
	select {
	case <-g.finish:
	case <-ctx.Done():
	}
	return Outcome{PlayerSessions: g.joined, Log: []any{"winner", "nobody"}}, ctx.Err()
}

// LastTick is called by the health check on another goroutine; a game that sets it from its loop uses an atomic.
func (g *toyGame) LastTick() time.Time { return time.Now() }
func (g *toyGame) Close() error        { return nil }

// join is what a real game does when a player connects (step 7).
func (g *toyGame) join(id string) error {
	if err := g.setup.Validator.Validate(id); err != nil {
		return err
	}
	g.joined = append(g.joined, id)
	return nil
}

func TestHostRunsAnyGame(t *testing.T) {
	p := &fakePlatform{rejectToken: "stranger"}
	var toy *toyGame
	h := New(p, slog.New(slog.NewTextHandler(io.Discard, nil)), func(s Setup) (Game, error) {
		toy = &toyGame{setup: s, finish: make(chan struct{})}
		return toy, nil
	})
	h.port = 7777 // RunProcess sets this from ProcessOptions.Port
	err := h.StartSession(context.Background(), Session{ID: "gs", Properties: map[string]string{"mode": "duel"}, Humans: 1})
	if err != nil {
		t.Fatal(err)
	}
	if toy.setup.Properties["mode"] != "duel" || toy.setup.Humans != 1 || toy.setup.Port != 7777 {
		t.Fatalf("the game got setup %+v; want the session's properties, player count, and port", toy.setup)
	}
	if h.Addr().String() != "127.0.0.1:4242" || !p.has("activate") {
		t.Fatalf("addr = %v, activated = %v; want the game's address and an activated session", h.Addr(), p.has("activate"))
	}
	if err := toy.join("stranger"); err == nil {
		t.Fatal("the validator let in a player session the service rejected")
	}
	if err := toy.join("psess-1"); err != nil {
		t.Fatal(err)
	}
	close(toy.finish)
	waitDone(t, h)
	if !p.has("remove:psess-1") || p.has("remove:stranger") || p.endings != 1 {
		t.Fatalf("calls = %v, endings = %d; want only the accepted player removed and one ProcessEnding", p.calls, p.endings)
	}
}

func TestNewGameThatReturnsNothingEndsProcess(t *testing.T) {
	p := &fakePlatform{}
	h := New(p, slog.New(slog.NewTextHandler(io.Discard, nil)), func(Setup) (Game, error) { return nil, nil })
	if err := h.StartSession(context.Background(), Session{ID: "gs"}); err == nil {
		t.Fatal("a session started without a game")
	}
	waitDone(t, h)
	if p.has("activate") || p.endings != 1 {
		t.Fatalf("activated = %v, endings = %d; want no activation and one ProcessEnding", p.has("activate"), p.endings)
	}
}

// PongGame listens on the port that ProcessReady announced, not the one in its base settings.
func TestPongGameListensOnTheAnnouncedPort(t *testing.T) {
	g, err := PongGame(game.Config{Addr: "127.0.0.1:1"})(Setup{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	g, err = PongGame(game.Config{Addr: "127.0.0.1:1"})(Setup{Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	addr, err := g.Listen()
	if err != nil || int(addr.Port()) != port {
		t.Fatalf("listened on %v (error %v); want port %d", addr, err, port)
	}
}

// A game session that arrives after the service asked this process to end must be refused, not activated.
func TestSessionAfterTerminateIsRefused(t *testing.T) {
	p := &fakePlatform{}
	h := newHost(p)
	h.Terminate()
	if err := h.StartSession(context.Background(), Session{ID: "gs-1"}); err == nil {
		t.Fatal("a session was accepted after Terminate")
	}
	if p.has("activate") {
		t.Error("ActivateGameSession was called on a process that is ending")
	}
}
