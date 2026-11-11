package gamelift

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amazon-gamelift/amazon-gamelift-servers-go-server-sdk/v5/model"
	"github.com/amazon-gamelift/amazon-gamelift-servers-go-server-sdk/v5/server"
)

// ProcessOptions describes how this game server process registers with Amazon GameLift Servers.
type ProcessOptions struct {
	// Port is the port players connect to. On a managed fleet the fleet's runtime configuration passes it
	// as a launch parameter, and it must be inside the fleet's port range.
	Port int
	// TLS serves wss:// with the fleet's certificate. Use it on a fleet created with TLS certificate generation.
	TLS bool
	// LogDir is a directory the service collects when a game session ends. On a managed fleet it must be an
	// absolute path under /local/game.
	LogDir string
}

// RunProcess registers this process with Amazon GameLift Servers, hosts one game session, and returns when
// the process may exit. It contains every server SDK call in the order the service expects them.
func RunProcess(ctx context.Context, h *Host, opts ProcessOptions) error {
	// Step 1. InitSDK connects this process to the service. Call it first, before any other SDK call.
	//
	// On a managed EC2 fleet the service sets the connection settings in the environment of every process it
	// starts. On an Anywhere fleet you set them yourself (scripts/play.sh does): the service endpoint, the
	// fleet ID, the compute name, a process ID, and an auth token from GetComputeAuthToken. InitSDKFromEnvironment
	// reads the same variables in both cases, so the code doesn't change between fleet types.
	if err := server.InitSDKFromEnvironment(); err != nil {
		return fmt.Errorf("init server SDK: %w", err)
	}
	h.log.Info("step 1: InitSDK connected this process to Amazon GameLift Servers")
	// Destroy frees the SDK. Call it after ProcessEnding, just before the process exits.
	defer server.Destroy()

	// On a fleet with TLS certificate generation, the certificate is loaded when a game session starts
	// (StartSession). In testing, GetComputeCertificate failed when called before ProcessReady.
	h.port = opts.Port
	if opts.TLS {
		h.loadTLS = fleetTLS
	}

	// Step 1 (continued). ProcessReady tells the service this process can host a game session, which port players use,
	// and which callbacks to call. Call it once, after the process has finished loading. From now on the
	// service may call OnStartGameSession at any time, and calls OnHealthCheck about once a minute.
	err := server.ProcessReady(server.ProcessParameters{
		// Step 6: the service picked this process for a new match. Besides the game properties, the game
		// session carries the matchmaker data: the players FlexMatch put in the match. humansIn counts them,
		// so the server knows whether it must play the second seat (see the rule set in lib/matchmaking.ts).
		OnStartGameSession: func(gs model.GameSession) {
			if err := h.StartSession(ctx, Session{ID: gs.GameSessionID, Properties: gs.GameProperties, Humans: humansIn(gs)}); err != nil {
				h.log.Error("could not host the game session", "error", err)
			}
		},
		// Called when FlexMatch backfill adds players to a running session. This sample turns backfill off
		// (backfillMode MANUAL and no backfill requests), so a match is only the players FlexMatch put
		// together, and there is nothing to do. A lone player is not joined by a late opponent: the server
		// plays that seat for the whole match.
		OnUpdateGameSession: func(model.UpdateGameSession) {},
		// The service needs this process to stop, for example because the instance is being scaled in.
		OnProcessTerminate: h.Terminate,
		// Return false if this process can no longer host players; the service then replaces it.
		OnHealthCheck: h.Healthy,
		Port:          opts.Port,
		LogParameters: server.LogParameters{LogPaths: []string{opts.LogDir}},
	})
	if err != nil {
		return fmt.Errorf("process ready: %w", err)
	}
	h.log.Info("step 1: ProcessReady sent; waiting for the service to start a game session", "port", opts.Port)

	// Wait for the match to end (endMatch calls ProcessEnding) or for a stop signal.
	select {
	case <-h.Done():
	case <-ctx.Done():
		h.Terminate()
	}
	return nil
}

// fleetTLS loads the fleet's certificate. Call it only after ProcessReady. Players connect with wss:// to the
// game session's DNS name, which matches the certificate, so the connection is encrypted, which helps protect it
// from being read or changed on the way. The folder holds certificate.pem, certificateChain.pem, and
// privateKey.pem; the full chain is the certificate followed by the chain.
func fleetTLS() (*tls.Config, error) {
	res, err := server.GetComputeCertificate()
	if err != nil {
		return nil, fmt.Errorf("get compute certificate: %w", err)
	}
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(res.CertificatePath, name)) }
	cert, err := read("certificate.pem")
	if err != nil {
		return nil, err
	}
	chain, err := read("certificateChain.pem")
	if err != nil {
		return nil, err
	}
	key, err := read("privateKey.pem")
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(append(append(cert, '\n'), chain...), key)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

// humansIn counts the players FlexMatch put in the match, from the game session's matchmaker data.
//
// Why the server needs this: the rule set (lib/matchmaking.ts) asks for two players, but after WAIT_FOR_OPPONENT_SECONDS (15 by default)
// without a second one it accepts a match of one. FlexMatch then creates a game session that has a single
// player session, and the only place the game server can learn that is the matchmaker data, a JSON document
// that FlexMatch fills in when it forms the match. It lists every team and the players on it, for example:
//
//	{"matchId":"...","teams":[{"name":"players","players":[{"playerId":"alice"}]}]}
//
// The SDK's model.MatchmakerData flattens that into a list of players. It returns 0 when the data is missing
// or can't be read (for example a game session created without matchmaking); the server then treats the match
// as a full one and waits for two players, which is the safe choice.
func humansIn(gs model.GameSession) int {
	var data model.MatchmakerData
	if err := data.UnmarshalJSON([]byte(gs.MatchmakerData)); err != nil {
		return 0
	}
	return len(data.Players)
}
