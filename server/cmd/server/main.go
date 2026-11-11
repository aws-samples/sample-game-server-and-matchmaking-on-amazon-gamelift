// Command server runs one two-player match.
//
// With -gamelift it runs as an Amazon GameLift Servers game server process: it reports to the service, hosts
// the one game session the service gives it, accepts only valid player session IDs, and exits when the match
// ends. The Amazon GameLift Servers code is in package gamelift; start with gamelift/doc.go.
//
// Without -gamelift it runs one match locally on the loopback address and accepts any join token, which is
// handy for working on the game without AWS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gameserver/game"
	"gameserver/gamelift"
)

const (
	// Ports a game server process may use on an Amazon GameLift Servers fleet.
	minFleetPort = 1024
	maxFleetPort = 60000

	// Managed fleets collect logs from under /local/game, where the build is installed.
	defaultFleetLogDir = "/local/game/logs"
)

func main() {
	useGameLift := flag.Bool("gamelift", false, "run as an Amazon GameLift Servers game server process")
	addr := flag.String("addr", "127.0.0.1:7777", "listen address (local mode)")
	port := flag.Int("port", 0, "port to listen on (-gamelift mode); the fleet passes it and it must be in the fleet's port range")
	useTLS := flag.Bool("tls", false, "serve wss:// with the fleet's TLS certificate (-gamelift mode, on a fleet with certificate generation)")
	origins := flag.String("allow-origin", "http://127.0.0.1:8080,http://localhost:8080", "web page origins allowed to connect, separated by commas")
	bindHost := flag.String("bind-host", "127.0.0.1", "address to listen on with -gamelift; use 0.0.0.0 on a fleet where players connect, and limit who can reach it with the fleet's inbound permissions")
	score := flag.Int("score", gamelift.DefaultScoreToWin, "goals that end the match, unless the game session sets scoreToWin")
	logDir := flag.String("log-dir", "", "also write server.log in this directory (default with -gamelift: "+defaultFleetLogDir+")")
	flag.Parse()

	if *useGameLift && *logDir == "" {
		*logDir = defaultFleetLogDir
	}
	logger, closeLog := newLogger(*logDir)
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if *useGameLift {
		err = runGameLift(ctx, logger, *bindHost, *port, *score, *logDir, splitList(*origins), *useTLS)
	} else {
		err = runLocal(ctx, logger, *addr, *score, splitList(*origins))
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server failed", "error", err)
		closeLog()
		os.Exit(1)
	}
}

func runGameLift(ctx context.Context, logger *slog.Logger, bindHost string, port, score int, logDir string, origins []string, useTLS bool) error {
	if port < minFleetPort || port > maxFleetPort {
		return fmt.Errorf("-port must be from %d to %d, got %d", minFleetPort, maxFleetPort, port)
	}
	// This is where a game plugs in: PongGame makes the demo game for each game session. To host your own game,
	// replace this call with one that passes your own gamelift.NewGame (see server/gamelift/yourgame.go), and drop
	// the Pong-only flags such as -score and the local mode below.
	h := gamelift.New(gamelift.SDK{}, logger, gamelift.PongGame(game.Config{
		Addr:           net.JoinHostPort(bindHost, strconv.Itoa(port)),
		AllowedOrigins: origins,
		ScoreToWin:     score, // used when the game session sets no scoreToWin property
		Logger:         logger,
	}))
	return gamelift.RunProcess(ctx, h, gamelift.ProcessOptions{Port: port, LogDir: logDir, TLS: useTLS})
}

func runLocal(ctx context.Context, logger *slog.Logger, addr string, score int, origins []string) error {
	srv := game.New(game.Config{Addr: addr, AllowedOrigins: origins, ScoreToWin: score, Logger: logger})
	bound, err := srv.Listen()
	if err != nil {
		return err
	}
	logger.Info("waiting for two players", "addr", bound)
	res, err := srv.Run(ctx)
	logger.Info("done", "winner", res.Winner, "scores", res.Scores, "reason", res.Reason)
	return err
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// newLogger logs to stderr, and also to server.log in dir when dir is set and writable.
//
// The folder and file are readable by other users on purpose. On a managed fleet the service collects this folder
// with its own account, not the one that runs the game server; with owner-and-group-only permissions it reports
// "No Read Permission" and keeps no logs. The log holds no secrets: player session IDs are not written to it.
func newLogger(dir string) (*slog.Logger, func()) {
	var w io.Writer = os.Stderr
	closeLog := func() {}
	if dir != "" {
		// nosemgrep: go.lang.correctness.permissions.file_permission.incorrect-default-permission
		if err := os.MkdirAll(dir, 0o755); err == nil {
			// nosemgrep: go.lang.correctness.permissions.file_permission.incorrect-default-permission
			if f, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				w = io.MultiWriter(os.Stderr, f)
				closeLog = func() { f.Close() }
			}
		}
	}
	return slog.New(slog.NewTextHandler(w, nil)), closeLog
}
