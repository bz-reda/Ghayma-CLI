package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The tunnel gateway carries one TCP connection per WebSocket: the CLI dials
// GET <gateway>/v1/dial?target=<id> with the session token, and every binary
// frame is a slice of the byte stream in either direction. There is no
// multiplexing — a database client opening ten connections opens ten streams.

// tunnelPingInterval is how often an idle stream is pinged. Cloudflare cuts a
// WebSocket that says nothing for 100 seconds, and a psql session left open
// over lunch says nothing at all. A var so tests can shorten it; each stream
// takes its value when it is dialed, so a test changing it never races a
// running one.
var tunnelPingInterval = 30 * time.Second

// tunnelHandshakeTimeout bounds the upgrade itself; the stream that follows is
// deliberately unbounded.
const tunnelHandshakeTimeout = 15 * time.Second

// gatewayDialURL turns the session's gateway URL into the dial URL for one
// target. An http(s) gateway (what an httptest server hands out) becomes
// ws(s), and a bare host is assumed to be TLS.
func gatewayDialURL(gatewayURL, targetID string) (string, error) {
	if !strings.Contains(gatewayURL, "://") {
		gatewayURL = "wss://" + gatewayURL
	}
	u, err := url.Parse(gatewayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("unsupported gateway scheme " + u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/dial"
	u.RawQuery = "target=" + url.QueryEscape(targetID)
	return u.String(), nil
}

// DialTunnel opens one gateway stream for a target and returns it as a
// net.Conn. The dial goes through the CLI's fallback dialer, so a resolver
// handing out unreachable Cloudflare addresses is worked around here too.
func DialTunnel(ctx context.Context, gatewayURL, token, targetID string) (net.Conn, error) {
	endpoint, err := gatewayDialURL(gatewayURL, targetID)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		NetDialContext:   apiDialer.DialContext,
		HandshakeTimeout: tunnelHandshakeTimeout,
	}
	ws, resp, err := dialer.DialContext(ctx, endpoint, http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		return nil, tunnelDialError(resp, err)
	}
	return newTunnelConn(ws), nil
}

// tunnelDialError turns a refused upgrade into the message the user needs: an
// expired session says how to get a new one, anything else passes the
// gateway's own words through.
func tunnelDialError(resp *http.Response, err error) error {
	if resp == nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("tunnel session expired — run ghayma connect --local again")
	}
	return decodeAPIError(resp)
}

// tunnelConn adapts a WebSocket to net.Conn: binary frames in both directions,
// partial reads served from the message being read, and a ping loop keeping an
// idle stream alive.
type tunnelConn struct {
	ws       *websocket.Conn
	reader   io.Reader // the message currently being read, if any
	pingEach time.Duration
	writeMu  sync.Mutex
	closed   chan struct{}
	once     sync.Once
}

func newTunnelConn(ws *websocket.Conn) *tunnelConn {
	c := &tunnelConn{ws: ws, pingEach: tunnelPingInterval, closed: make(chan struct{})}
	ws.SetPongHandler(func(string) error { return nil })
	go c.keepalive()
	return c
}

// Read serves bytes from the message in flight, taking the next one when it
// runs out. Non-binary frames are not part of the stream and are skipped.
func (c *tunnelConn) Read(p []byte) (int, error) {
	for {
		if c.reader != nil {
			n, err := c.reader.Read(p)
			if n > 0 {
				return n, nil
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return 0, tunnelReadError(err)
			}
			c.reader = nil
		}
		kind, reader, err := c.ws.NextReader()
		if err != nil {
			return 0, tunnelReadError(err)
		}
		if kind == websocket.BinaryMessage {
			c.reader = reader
		}
	}
}

// tunnelReadError reports the far side hanging up as a plain end of stream, so
// a copy loop finishes instead of printing a WebSocket close code.
func tunnelReadError(err error) error {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
		return io.EOF
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.EOF
	}
	return err
}

func (c *tunnelConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *tunnelConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		// Best effort: a close frame lets the gateway drop its own connection
		// to the database now rather than when the socket eventually dies.
		c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	})
	return c.ws.Close()
}

func (c *tunnelConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *tunnelConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }

func (c *tunnelConn) SetDeadline(t time.Time) error {
	return errors.Join(c.ws.SetReadDeadline(t), c.ws.SetWriteDeadline(t))
}
func (c *tunnelConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *tunnelConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

// keepalive pings while the stream is open. WriteControl is the one write that
// may race with Write, so no lock is taken here.
func (c *tunnelConn) keepalive() {
	ticker := time.NewTicker(c.pingEach)
	defer ticker.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}
}

// PipeTunnel copies bytes both ways until either side closes, then closes both
// so the other copy cannot hang, and returns once neither is running.
func PipeTunnel(local net.Conn, remote net.Conn) {
	var once sync.Once
	stop := func() {
		once.Do(func() {
			local.Close()
			remote.Close()
		})
	}
	var wg sync.WaitGroup
	for _, way := range [][2]net.Conn{{remote, local}, {local, remote}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			io.Copy(way[0], way[1])
			stop()
		}()
	}
	wg.Wait()
}
