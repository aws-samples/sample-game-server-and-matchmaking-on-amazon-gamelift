package gamelift

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

// Platform is the part of the Amazon GameLift Servers server SDK that a game session uses. SDK implements
// it with the real SDK; tests use a fake.
type Platform interface {
	// ActivateGameSession tells the service this process is ready for the session's players (step 6).
	ActivateGameSession() error
	// AcceptPlayerSession checks that a player session ID belongs to this game session and marks the player
	// connected (step 7). It fails for an unknown, expired, or already used player session.
	AcceptPlayerSession(playerSessionID string) error
	// RemovePlayerSession marks a player as gone after the match, so the service's player counts stay correct.
	RemovePlayerSession(playerSessionID string) error
	// ProcessEnding tells the service this process is shutting down after the match.
	ProcessEnding() error
}

// Session is the game session the service asks this process to host.
type Session struct {
	ID string
	// Properties are the game properties from the matchmaking configuration, for example scoreToWin.
	Properties map[string]string
	// Humans is how many players FlexMatch put in the match, read from the game session's matchmaker data.
	// The rule set waits up to WAIT_FOR_OPPONENT_SECONDS (15 by default) for a second player and then accepts a match of one; with Humans = 1
	// the game server plays the other seat. 0 means unknown (no matchmaker data), which is handled as a full
	// match of two.
	Humans int
}

const (
	// healthStaleAfter is how long the match loop may go without a tick before the process reports unhealthy.
	healthStaleAfter = 10 * time.Second
	// stopTimeout bounds how long Terminate waits for a running match to stop.
	stopTimeout = 30 * time.Second
)

// Host is the generic part of a game server process. It does not know your game: it asks NewGame (yourgame.go) for
// one Game per game session, and runs the session around it: StartSession (step 6) before the match, endMatch after
// it, plus Terminate and Healthy for the service's callbacks. process.go connects these methods to the server SDK.
// One process hosts one session and then exits: this keeps each match isolated, and a new
// process replaces it.
type Host struct {
	platform Platform
	newGame  NewGame // makes your game for each game session (yourgame.go)
	log      *slog.Logger
	port     int // the port ProcessReady announces; set by RunProcess

	loadTLS func() (*tls.Config, error) // set on a fleet with TLS certificates
	now     func() time.Time            // the clock; tests replace it to simulate a stuck match

	mu       sync.Mutex
	stopping bool // set by Terminate
	match    Game
	addr     netip.AddrPort
	cancel   context.CancelFunc

	ending sync.Once
	done   chan struct{}
	exit   sync.Once
}

// New returns a host. newGame makes your game for each game session: PongGame is the demo game's, and
// yourgame.go explains how to write your own.
func New(platform Platform, log *slog.Logger, newGame NewGame) *Host {
	if log == nil {
		log = slog.Default()
	}
	return &Host{platform: platform, newGame: newGame, log: log, now: time.Now, done: make(chan struct{})}
}

// Addr returns the address players connect to, once a session has started. Only the tests use it.
func (h *Host) Addr() netip.AddrPort {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addr
}

// Done is closed after the process has called ProcessEnding and may exit.
func (h *Host) Done() <-chan struct{} { return h.done }

// StartSession handles OnStartGameSession (step 6): the service has placed a match on this process.
// Get everything ready for players first, then call ActivateGameSession. Players receive the server
// address only after the session is active, so anything that isn't ready yet would fail their first message.
func (h *Host) StartSession(ctx context.Context, s Session) error {
	h.mu.Lock()
	if h.match != nil || h.stopping {
		h.mu.Unlock()
		return errors.New("this process already hosts a game session or is ending")
	}
	// Give your game what it needs from the session (see Setup in yourgame.go): the game properties the
	// matchmaking configuration set, how many players FlexMatch put in the match, and the check for each
	// player who connects. With one player the game plays the empty seat itself; the service doesn't need to
	// know about it.
	setup := Setup{
		Properties: s.Properties,
		Humans:     s.Humans,
		Port:       h.port,
		Validator:  playerSessionValidator{h.platform, h.log},
	}
	var err error
	if h.loadTLS != nil {
		if setup.TLS, err = h.loadTLS(); err != nil {
			h.mu.Unlock()
			h.endProcess()
			return fmt.Errorf("load the fleet's TLS certificate: %w", err)
		}
		h.log.Info("step 6: GetComputeCertificate loaded the fleet's TLS certificate; players connect with wss://")
	}
	match, err := h.newGame(setup)
	if err == nil && match == nil {
		err = errors.New("NewGame returned no game and no error")
	}
	if err != nil {
		h.mu.Unlock()
		h.endProcess() // a session this process can't host: end it so a clean process starts
		return err
	}
	addr, err := match.Listen() // open the port before activating
	if err != nil {
		h.mu.Unlock()
		h.endProcess()
		return fmt.Errorf("listen: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	h.match, h.addr, h.cancel = match, addr, cancel
	h.mu.Unlock()

	if err := h.platform.ActivateGameSession(); err != nil {
		cancel()
		match.Close()
		h.endProcess()
		return fmt.Errorf("activate game session: %w", err)
	}
	h.log.Info("step 6: OnStartGameSession received; port open, ActivateGameSession sent", "gameSessionId", s.ID, "properties", s.Properties, "humans", s.Humans)
	go h.endMatch(runCtx, match, s.ID)
	return nil
}

// endMatch plays the match and then ends it: remove each player session, then call ProcessEnding.
func (h *Host) endMatch(ctx context.Context, match Game, sessionID string) {
	res, err := match.Run(ctx)
	attrs := append([]any{"gameSessionId", sessionID}, res.Log...)
	h.log.Info("match over", append(attrs, "error", err)...)
	for _, playerSessionID := range res.PlayerSessions {
		if err := h.platform.RemovePlayerSession(playerSessionID); err != nil {
			h.log.Warn("remove player session failed", "error", err)
		}
	}
	h.log.Info("after the match: RemovePlayerSession sent for each player", "players", len(res.PlayerSessions))
	h.endProcess()
}

// Terminate handles OnProcessTerminate: stop a running match and end the process. The service gives the
// process a few minutes to call ProcessEnding before it stops the process itself.
func (h *Host) Terminate() {
	h.mu.Lock()
	h.stopping = true // a StartSession that arrives from now on is refused
	cancel, running := h.cancel, h.match != nil
	h.mu.Unlock()
	if !running {
		h.endProcess()
		return
	}
	cancel() // endMatch removes the player sessions and calls ProcessEnding when the match stops
	select {
	case <-h.done:
	case <-time.After(stopTimeout):
		h.log.Warn("match did not stop in time")
		h.endProcess()
	}
}

// Healthy handles OnHealthCheck. It returns false when a running match has stopped ticking, so the service
// replaces a stuck process instead of sending it more players. The service waits up to a minute for an
// answer; keep this check fast.
func (h *Host) Healthy() bool {
	h.mu.Lock()
	match := h.match
	h.mu.Unlock()
	if match == nil {
		return true // idle, waiting for a game session
	}
	select {
	case <-h.done:
		return true // finished; the process is about to exit
	default:
	}
	last := match.LastTick()
	return last.IsZero() || h.now().Sub(last) < healthStaleAfter
}

// endProcess calls ProcessEnding once and lets RunProcess return.
func (h *Host) endProcess() {
	h.ending.Do(func() {
		if err := h.platform.ProcessEnding(); err != nil {
			h.log.Warn("process ending failed", "error", err)
			return
		}
		h.log.Info("after the match: ProcessEnding sent; this process exits and a new one starts for the next match")
	})
	h.exit.Do(func() { close(h.done) })
}

// playerSessionValidator is step 7. A player joins by sending the player session ID they got from
// matchmaking; AcceptPlayerSession confirms it belongs to this game session and hasn't been used. This is
// what stops someone who knows the server's address from taking a seat. The match runs this check on the
// player's connection, not in the match loop, because it is a call to the service.
type playerSessionValidator struct {
	p   Platform
	log *slog.Logger
}

func (v playerSessionValidator) Validate(playerSessionID string) error {
	if err := v.p.AcceptPlayerSession(playerSessionID); err != nil {
		v.log.Warn("step 7: AcceptPlayerSession refused a player", "error", err)
		return err
	}
	v.log.Info("step 7: AcceptPlayerSession accepted a player")
	return nil
}
