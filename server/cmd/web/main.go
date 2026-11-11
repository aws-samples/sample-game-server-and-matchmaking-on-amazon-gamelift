// Command web serves the browser client on this machine. The page signs in, finds a match through the player
// API, and then connects straight to the game server with a WebSocket.
//
// With -api-url, -region, and -client-id (from the stack outputs) the page signs in and uses matchmaking.
// Without them it runs in local mode: no sign-in, and it joins the local game server at -local-server.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

//go:embed page
var pageFiles embed.FS

// pageConfig tells the page how to find a match.
type pageConfig struct {
	Mode        string `json:"mode"` // "online" or "local"
	APIURL      string `json:"apiUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
	LocalServer string `json:"localServer,omitempty"`
}

var (
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	clientIDPattern = regexp.MustCompile(`^[a-z0-9]{1,128}$`)
)

// newPageConfig checks the settings. The three online settings go together: all of them or none.
func newPageConfig(apiURL, region, clientID, localServer string) (pageConfig, error) {
	if apiURL == "" && region == "" && clientID == "" {
		return pageConfig{Mode: "local", LocalServer: localServer}, nil
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return pageConfig{}, errors.New("-api-url must be an https URL such as https://abc123.execute-api.us-west-2.amazonaws.com")
	}
	if !regionPattern.MatchString(region) {
		return pageConfig{}, errors.New("-region must be an AWS Region code such as us-west-2")
	}
	if !clientIDPattern.MatchString(clientID) {
		return pageConfig{}, errors.New("-client-id must be the user pool app client ID")
	}
	return pageConfig{Mode: "online", APIURL: strings.TrimRight(apiURL, "/"), Region: region, ClientID: clientID}, nil
}

// contentSecurityPolicy lets the page load only its own files and talk only to the player API, the user pool,
// and game servers.
func (c pageConfig) contentSecurityPolicy() string {
	connect := "'self' ws://127.0.0.1:* ws://localhost:* wss:"
	if c.Mode == "online" {
		u, _ := url.Parse(c.APIURL)
		connect += fmt.Sprintf(" https://%s https://cognito-idp.%s.amazonaws.com", u.Host, c.Region)
	}
	return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'none'; " +
		"frame-ancestors 'none'; base-uri 'none'; connect-src " + connect
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to serve the page on")
	apiURL := flag.String("api-url", "", "player API URL (stack output)")
	region := flag.String("region", "", "AWS Region of the user pool")
	clientID := flag.String("client-id", "", "user pool app client ID (stack output)")
	localServer := flag.String("local-server", "ws://127.0.0.1:7777", "game server to join in local mode")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := newPageConfig(*apiURL, *region, *clientID, *localServer)
	if err != nil {
		logger.Error("invalid settings", "error", err)
		os.Exit(2)
	}
	page, _ := fs.Sub(pageFiles, "page")
	csp := cfg.contentSecurityPolicy()
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(page))
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	logger.Info("open this page in a browser", "url", "http://"+*listen+"/", "mode", cfg.Mode)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("web server failed", "error", err)
		os.Exit(1)
	}
}
