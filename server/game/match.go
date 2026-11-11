package game

// match.go runs one match: players connect over WebSocket, send their moves, and receive the state 60 times a
// second. The server decides everything; a client only says "up", "down", or "stay".
//
// This package is the demo game and the part you replace with your own (see gamelift/yourgame.go, which wraps a
// game for the lifecycle code). What a game server needs to do for Amazon GameLift Servers is small and is marked
// in this file:
//   - Listen opens the port, and Run plays the match until it ends or ctx is canceled;
//   - serveWS calls Validator.Validate before it gives a connecting player a seat (step 7);
//   - Run returns the player session IDs it accepted, so the lifecycle code can remove them afterwards;
//   - LastTick lets the health check see that the loop is still running.
//
// Messages are JSON:
//
//	client -> server  {"type":"join","playerSessionId":"..."}   first message, once
//	                  {"type":"move","dir":-1}                  -1 up, 0 stay, 1 down
//	server -> client  {"type":"joined","seat":0}                 0 left, 1 right
//	                  {"type":"rejected","reason":"..."}
//	                  {"type":"state", ...Pong fields}

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Validator decides whether a player session ID may take a seat. On Amazon GameLift Servers it calls
// AcceptPlayerSession; locally, AllowAll accepts every ID.
type Validator interface {
	Validate(playerSessionID string) error
}

// AllowAll accepts every player session ID. Use it for local runs only.
type AllowAll struct{}

func (AllowAll) Validate(string) error { return nil }

// Config controls one match. Zero values get the defaults noted on each field.
type Config struct {
	Addr           string        // TCP listen address. Default: 127.0.0.1:7777
	AllowedOrigins []string      // web page origins that may connect; clients that send no Origin (not browsers) may always connect
	TLS            *tls.Config   // serve encrypted WebSocket (wss) instead of plain WebSocket when set
	ScoreToWin     int           // Default: 7
	JoinTimeout    time.Duration // give up if both players haven't joined in this time. Default: 2m
	EndGrace       time.Duration // keep the final state on screen this long before closing. Default: 2s
	MaxConnections int           // open connections the port accepts in total. Default: 32
	MaxPerSource   int           // open connections from one address. Default: 4
	Humans         int           // players in this match: 2 (default), or 1 to let the server play the other seat (computer player)
	Validator      Validator     // Default: AllowAll
	Logger         *slog.Logger  // Default: slog.Default()
}

// Result describes how a match ended.
type Result struct {
	Winner         int // -1 when there is none
	Scores         [2]int
	Reason         string   // "completed", "forfeit", "no players", or "canceled"
	PlayerSessions []string // every player session ID that was accepted, to remove after the match
}

const (
	tickRate       = 60
	maxMessage     = 512 // bytes
	readTimeout    = 15 * time.Second
	maxPendingJoin = 8   // join checks in flight at once
	computerSkill  = 0.6 // how fast the computer player reacts, from 0.1 to 1; below 1 so a person can win
)

type clientMsg struct {
	Type            string  `json:"type"`
	PlayerSessionID string  `json:"playerSessionId"`
	Dir             float64 `json:"dir"`
}

type player struct {
	playerSessionID string
	move            float64
	computer        bool        // a seat the server plays itself: no connection, no player session
	out             chan []byte // messages for this player; the connection's writer drains it
}

// events from connections to the match loop; only the loop touches the match state
type (
	seatRequest struct {
		p     *player
		reply chan int // the seat, or -1
	}
	moveEvent struct {
		seat int
		dir  float64
	}
	leaveEvent struct{ seat int }
)

// Server runs one match.
type Server struct {
	cfg      Config
	listener net.Listener
	events   chan any
	pending  chan struct{}
	stopped  chan struct{} // closed when Run returns, so connection goroutines never wait on a loop that has ended
	lastTick atomic.Int64

	mu    sync.Mutex
	conns []*websocket.Conn
}

func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:7777"
	}
	if cfg.ScoreToWin <= 0 {
		cfg.ScoreToWin = 7
	}
	if cfg.JoinTimeout <= 0 {
		cfg.JoinTimeout = 2 * time.Minute
	}
	if cfg.EndGrace <= 0 {
		cfg.EndGrace = 2 * time.Second
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = defaultMaxConnections
	}
	if cfg.MaxPerSource <= 0 {
		cfg.MaxPerSource = defaultMaxPerSource
	}
	if cfg.Humans != 1 {
		cfg.Humans = 2
	}
	if cfg.Validator == nil {
		cfg.Validator = AllowAll{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{cfg: cfg, events: make(chan any, 256), pending: make(chan struct{}, maxPendingJoin), stopped: make(chan struct{})}
}

// Listen opens the port and returns its address. Call it before telling anyone where to connect.
func (s *Server) Listen() (netip.AddrPort, error) {
	l, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return netip.AddrPort{}, err
	}
	l = limitListener(l, s.cfg.MaxConnections, s.cfg.MaxPerSource) // before TLS, so a flood cannot cost a handshake each
	if s.cfg.TLS != nil {
		l = tls.NewListener(l, s.cfg.TLS)
	}
	s.listener = l
	return l.Addr().(*net.TCPAddr).AddrPort(), nil
}

// Close releases the port. Run closes it itself; call Close only if you called Listen and not Run.
func (s *Server) Close() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

// LastTick is when the match loop last ran, so a health check can spot a stuck match.
func (s *Server) LastTick() time.Time {
	if n := s.lastTick.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// Run plays the match and returns when it ends, when nobody joins in time, or when ctx is canceled.
// Call Run once per Server.
func (s *Server) Run(ctx context.Context) (Result, error) {
	if s.listener == nil {
		if _, err := s.Listen(); err != nil {
			return Result{Winner: -1, Reason: "canceled"}, err
		}
	}
	httpServer := &http.Server{Handler: http.HandlerFunc(s.serveWS), ReadHeaderTimeout: 5 * time.Second}
	go httpServer.Serve(s.listener)
	defer s.shutdown(httpServer)
	defer close(s.stopped)

	pong := NewPong(s.cfg.ScoreToWin)
	var seats [2]*player
	var accepted []string
	result := func(reason string) Result {
		return Result{Winner: pong.Winner, Scores: pong.Score, Reason: reason, PlayerSessions: accepted}
	}
	reason := "completed"
	start := time.Now()
	var finishedAt time.Time
	ticker := time.NewTicker(time.Second / tickRate)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return result("canceled"), ctx.Err()

		case ev := <-s.events:
			switch ev := ev.(type) {
			case seatRequest:
				seat := -1
				inUse := slices.ContainsFunc(seats[:], func(p *player) bool { return p != nil && p.playerSessionID == ev.p.playerSessionID })
				if !inUse && pong.Phase == Waiting {
					seat = slices.Index(seats[:], nil)
				}
				if seat >= 0 {
					seats[seat] = ev.p
					accepted = append(accepted, ev.p.playerSessionID)
					s.cfg.Logger.Info("player seated", "seat", seat)
					if s.cfg.Humans == 1 && seat == 0 {
						// FlexMatch found no second player within the rule set's wait (WAIT_FOR_OPPONENT_SECONDS in
						// lib/matchmaking.ts) and started the match with one. Seat the
						// computer player in seat 1 now, so the "both seats taken" check below starts the match.
						// The computer has no connection (out is nil) and AIMove moves its paddle on every tick.
						seats[1] = &player{computer: true}
						s.cfg.Logger.Info("no second player: the computer plays seat 1")
					}
					if seats[0] != nil && seats[1] != nil {
						pong.Start()
						s.cfg.Logger.Info("match started")
					}
				}
				ev.reply <- seat
			case moveEvent:
				if p := seats[ev.seat]; p != nil {
					p.move = ev.dir
				}
			case leaveEvent:
				if pong.Phase == Waiting {
					// Free the seat; nobody has played yet. On Amazon GameLift Servers the same player session ID is accepted
					// only once, so the player cannot return: the seat helps only a different player (or local mode), and a
					// lone player waits for JoinTimeout.
					seats[ev.seat] = nil
				} else if pong.Phase != Finished {
					s.cfg.Logger.Warn("player left", "seat", ev.seat)
					pong.Forfeit(ev.seat)
					reason = "forfeit"
				}
			}

		case now := <-ticker.C:
			if pong.Phase == Waiting && now.Sub(start) > s.cfg.JoinTimeout {
				return result("no players"), nil
			}
			var move [2]float64
			for i, p := range seats {
				if p != nil {
					move[i] = p.move
					if p.computer {
						move[i] = AIMove(pong, i, computerSkill)
					}
				}
			}
			pong.Step(1.0/tickRate, move)
			state, _ := json.Marshal(struct {
				Type string `json:"type"`
				*Pong
			}{"state", pong})
			for _, p := range seats {
				if p != nil {
					send(p, state)
				}
			}
			s.lastTick.Store(now.UnixNano())

			if pong.Phase == Finished {
				if finishedAt.IsZero() {
					finishedAt = now
					s.cfg.Logger.Info("match finished", "winner", pong.Winner, "score", pong.Score, "reason", reason)
				}
				if now.Sub(finishedAt) >= s.cfg.EndGrace {
					return result(reason), nil
				}
			}
		}
	}
}

// serveWS handles one player's connection: check the origin, read the join, check the player session, take a
// seat, then forward moves to the match loop until the connection closes.
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: s.allowedOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has already answered with an error
	}
	s.track(conn)
	defer s.untrack(conn)
	defer conn.Close()
	conn.SetReadLimit(maxMessage)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var join clientMsg
	if err := conn.ReadJSON(&join); err != nil || join.Type != "join" || join.PlayerSessionID == "" || len(join.PlayerSessionID) > 128 {
		return
	}
	select {
	case s.pending <- struct{}{}: // limit how many checks run at once
	default:
		conn.WriteJSON(map[string]string{"type": "rejected", "reason": "busy"})
		return
	}
	err = s.cfg.Validator.Validate(join.PlayerSessionID)
	<-s.pending
	if err != nil {
		conn.WriteJSON(map[string]string{"type": "rejected", "reason": "invalid player session"})
		return
	}

	p := &player{playerSessionID: join.PlayerSessionID, out: make(chan []byte, 8)}
	reply := make(chan int, 1)
	if !s.toLoop(seatRequest{p, reply}) {
		return
	}
	var seat int
	select {
	case seat = <-reply:
	case <-s.stopped:
		return
	}
	if seat < 0 {
		conn.WriteJSON(map[string]string{"type": "rejected", "reason": "match is full"})
		return
	}
	conn.WriteJSON(map[string]any{"type": "joined", "seat": seat})
	gone := make(chan struct{})
	defer close(gone) // stops the writer when this connection ends
	go func() {       // the only writer on this connection from here on
		for {
			select {
			case msg := <-p.out:
				conn.SetWriteDeadline(time.Now().Add(time.Second))
				if conn.WriteMessage(websocket.TextMessage, msg) != nil {
					return
				}
			case <-gone:
				return
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		var msg clientMsg
		if err := conn.ReadJSON(&msg); err != nil {
			s.toLoop(leaveEvent{seat})
			return
		}
		if msg.Type == "move" {
			if !s.toLoop(moveEvent{seat, msg.Dir}) {
				return
			}
		}
	}
}

// allowedOrigin lets a web page connect only if its origin is listed. Clients that send no Origin header are
// not browsers (a native game client, the bot), and a web page can't hide its origin.
func (s *Server) allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || slices.Contains(s.cfg.AllowedOrigins, origin)
}

// send queues a message without ever blocking the match loop; a player too slow to keep up misses a frame.
func send(p *player, msg []byte) {
	select {
	case p.out <- msg:
	default:
	}
}

func (s *Server) track(c *websocket.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) untrack(c *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = slices.DeleteFunc(s.conns, func(x *websocket.Conn) bool { return x == c })
}

func (s *Server) shutdown(h *http.Server) {
	h.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

// toLoop hands an event to the match loop. It returns false when the loop has ended, so a connection goroutine
// that is still checking a player session (a service call) cannot block forever after Run returns.
func (s *Server) toLoop(ev any) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.stopped:
		return false
	}
}
