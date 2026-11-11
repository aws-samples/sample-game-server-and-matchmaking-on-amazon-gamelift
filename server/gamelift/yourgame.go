package gamelift

// yourgame.go is where a game plugs into Amazon GameLift Servers. Everything else in this package (process.go,
// lifecycle.go, sdk.go) is generic: it reports the process to the service, hosts one game session, and ends the
// process. It knows nothing about Pong. To use your own game, keep those files and change this one.
//
// What the lifecycle code needs from a game is a factory (NewGame) and the four methods of the Game it makes. It
// calls them in this order:
//
//  a. NewGame(setup)  your factory makes the game when OnStartGameSession arrives (step 6).
//  b. Listen()        open the port players will connect to, and return its address.
//  c. (the lifecycle calls ActivateGameSession; from now on players can connect)
//  d. Run(ctx)        play the whole match. Return when it ends, or when ctx is canceled (the service asked this
//     process to stop). While it runs, call Setup.Validator.Validate for each player who connects (step 7).
//  e. (the lifecycle calls RemovePlayerSession for each ID in Outcome.PlayerSessions, then ProcessEnding)
//
// While Run is going, the health check (OnHealthCheck) calls LastTick, so the service can replace a process whose
// game loop is stuck. Close is only for the case where the session can't be activated.
//
// What stays your job, in your own game code (here, package game):
//   - the network protocol between your clients and your server (this sample uses WebSocket and JavaScript Object
//     Notation (JSON) messages; a console title usually uses User Datagram Protocol (UDP), or a platform's own
//     networking);
//   - the game rules and the game loop;
//   - deciding which connected player gets which seat;
//   - what happens when a player leaves in the middle of a match.
//
// Steps to switch to your game:
//  a. Write a type that implements Game for your server (the demo wraps game.Server below).
//  b. Write a NewGame function that reads the Setup and builds that type, like PongGame. Return an error if the
//     game properties are not usable: the lifecycle then ends this process cleanly and a new one starts.
//  c. In cmd/server/main.go, pass your NewGame to New instead of PongGame.
//  d. Set your own game properties in the matchmaking configuration (lib/matchmaking.ts, gameProperties); they
//     arrive in Setup.Properties.
//  e. For each client that connects, call Setup.Validator.Validate with the player session ID the client sends.
//     Refuse the player if it returns an error. This check keeps players who were not matched out of your match.
//     Validate calls the service and can take a moment, so don't call it from your game loop: with UDP, where one
//     socket serves all players, run it on its own goroutine or remember the result for each address.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"gameserver/game"
)

// Game is one match of your game, as the lifecycle code sees it. A new Game is made for each game session, and a
// process hosts one game session, so a Game never has to be reused or reset.
type Game interface {
	// Listen opens the port that players connect to and returns its address. The lifecycle calls it before
	// ActivateGameSession, because players receive the address only after the session is active.
	Listen() (netip.AddrPort, error)
	// Run plays the match and returns when it ends or when ctx is canceled. It must not call any Amazon
	// GameLift Servers function itself: the lifecycle does that, using the Outcome.
	//
	// When ctx is canceled (OnProcessTerminate does that), return within stopTimeout, 30 seconds; the process
	// ends then even if Run is still going. Return the Outcome with every player session ID you accepted
	// whether the match completed, ended early, or was canceled, because the lifecycle removes those sessions.
	// Run owns the goroutines it started and the port: stop the goroutines and close the port on every way out.
	Run(ctx context.Context) (Outcome, error)
	// LastTick is when the game loop last ran. The health check reports the process unhealthy if this is too
	// old while a match is running. Return the zero time before the loop has started. The health check calls
	// it from another goroutine, so it must be safe to call at any time (an atomic value is enough).
	LastTick() time.Time
	// Close releases the port. The lifecycle calls it only if the session could not be activated, before Run
	// starts. Once Run has started, Run closes the port itself.
	Close() error
}

// Outcome is what the lifecycle needs to know after a match.
type Outcome struct {
	// PlayerSessions are the IDs of every player session the game accepted. The lifecycle calls
	// RemovePlayerSession for each one, so the service's player counts stay correct.
	PlayerSessions []string
	// Log is extra key and value pairs for the "match over" log line, for example "winner", 1.
	Log []any
}

// PlayerValidator decides whether a player session ID may join. The lifecycle implements it with
// AcceptPlayerSession, which is step 7. Your game calls it when a player connects, before it gives the player
// a seat. It is safe to call from several goroutines.
type PlayerValidator interface {
	Validate(playerSessionID string) error
}

// Setup is what the lifecycle gives your game when a game session starts.
type Setup struct {
	// Properties are the game properties from the matchmaking configuration (lib/matchmaking.ts), for example
	// scoreToWin. Validate them: they come from your own deployment, but a wrong value should not start a bad match.
	Properties map[string]string
	// Humans is how many players FlexMatch put in the match, or 0 if unknown. With the rule set in this sample,
	// a match may have one player when nobody else joined within the wait time.
	Humans int
	// Port is the port this process told the service about in ProcessReady. Listen on it: players are sent to
	// that port, so a different one means nobody can connect. It is 0 only in tests.
	Port int
	// TLS is the certificate for wss:// on a fleet with TLS certificate generation, otherwise nil.
	TLS *tls.Config
	// Validator checks each connecting player's player session ID (step 7).
	Validator PlayerValidator
}

// NewGame makes the game for one game session. It runs inside OnStartGameSession, so keep it quick. Return a game
// or an error, never neither. An error ends this process cleanly, and a new one starts.
type NewGame func(Setup) (Game, error)

// The demo game: Pong, in package game.
const (
	// ScorePropertyKey is the game property that sets the goals needed to win. The matchmaking configuration in
	// lib/matchmaking.ts sets it, which shows how to pass a setting from the backend to the server.
	ScorePropertyKey  = "scoreToWin"
	DefaultScoreToWin = 7
	maxScoreToWin     = 20
)

// PongGame returns the NewGame of the demo game. base holds the settings that don't come from the session:
// the listen address, the allowed web page origins, and the logger.
func PongGame(base game.Config) NewGame {
	fallback := base.ScoreToWin // used when the game session sets no scoreToWin property
	if fallback <= 0 {
		fallback = DefaultScoreToWin
	}
	return func(s Setup) (Game, error) {
		score, err := parseScore(s.Properties[ScorePropertyKey], fallback)
		if err != nil {
			return nil, err
		}
		cfg := base
		if cfg.JoinTimeout == 0 {
			// A match whose players never connect holds this process, so give up sooner than the game's default.
			// The page connects as soon as the match is placed.
			cfg.JoinTimeout = 30 * time.Second
		}
		cfg.ScoreToWin = score
		if s.Port != 0 { // listen where ProcessReady told the service players will connect
			if host, _, err := net.SplitHostPort(cfg.Addr); err == nil {
				cfg.Addr = net.JoinHostPort(host, strconv.Itoa(s.Port))
			}
		}
		cfg.Humans = s.Humans
		cfg.TLS = s.TLS
		cfg.Validator = s.Validator
		return pong{game.New(cfg)}, nil
	}
}

// pong adapts game.Server to Game. Listen, LastTick, and Close already match; only Run's result differs.
type pong struct{ *game.Server }

func (p pong) Run(ctx context.Context) (Outcome, error) {
	res, err := p.Server.Run(ctx)
	return Outcome{
		PlayerSessions: res.PlayerSessions,
		Log:            []any{"winner", res.Winner, "scores", res.Scores, "reason", res.Reason},
	}, err
}

func parseScore(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maxScoreToWin {
		return 0, fmt.Errorf("game property %q must be a whole number from 1 to %d, got %q", ScorePropertyKey, maxScoreToWin, value)
	}
	return n, nil
}
