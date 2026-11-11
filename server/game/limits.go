package game

// limits.go caps how many connections the game port accepts, so a flood of idle or invalid connections cannot
// take every slot a real player needs. Two limits apply, both before the TLS handshake and before any message is read:
//   - at most MaxPerSource connections from one address, and
//   - at most MaxConnections connections in total.
// A match has two players, and each page keeps one connection, so the defaults leave room for players that share an
// address (for example one home network) and for a local-mode reconnect before the match starts. (On Amazon GameLift
// Servers a player session ID is accepted once, so a player who leaves cannot come back to the same match.)

import (
	"net"
	"sync"

	"golang.org/x/net/netutil"
)

const (
	defaultMaxConnections = 32
	defaultMaxPerSource   = 4
)

// limitListener wraps a listener with the two limits. The per-source check runs first, so one address cannot use up
// the slots of the total limit: its extra connections are closed right away.
func limitListener(l net.Listener, maxTotal, maxPerSource int) net.Listener {
	return netutil.LimitListener(&sourceLimitListener{Listener: l, max: maxPerSource, count: map[string]int{}}, maxTotal)
}

type sourceLimitListener struct {
	net.Listener
	max   int
	mu    sync.Mutex
	count map[string]int
}

func (l *sourceLimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		source, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		l.mu.Lock()
		over := l.count[source] >= l.max
		if !over {
			l.count[source]++
		}
		l.mu.Unlock()
		if over {
			c.Close() // refuse; wait for the next connection
			continue
		}
		return &sourceConn{Conn: c, release: func() { l.release(source) }}, nil
	}
}

func (l *sourceLimitListener) release(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count[source]--; l.count[source] <= 0 {
		delete(l.count, source)
	}
}

// sourceConn gives its slot back exactly once, when the connection closes.
type sourceConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *sourceConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}
