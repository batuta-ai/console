package client

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type eventLifecycleConn struct {
	net.Conn
	reading chan struct{}
	once    sync.Once
	closed  atomic.Bool
}

func (c *eventLifecycleConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.reading) })
	return c.Conn.Read(p)
}

func (c *eventLifecycleConn) Close() error {
	err := c.Conn.Close()
	c.closed.Store(true)
	return err
}

func TestPureEventReadCancellationDetaches(t *testing.T) {
	for _, mode := range []string{"pre-canceled", "expired deadline", "during read"} {
		t.Run(mode, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := &eventLifecycleConn{Conn: local, reading: make(chan struct{})}
			c := New("http://fixture", "secret")
			c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v0/events" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Fatalf("unexpected attachment: %v", r)
				}
				key := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: conn, Header: http.Header{
					"Connection":           {"Upgrade"},
					"Upgrade":              {"websocket"},
					"Sec-Websocket-Accept": {base64.StdEncoding.EncodeToString(key[:])},
				}}, nil
			})
			stream, err := c.DialEvents(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			switch mode {
			case "pre-canceled":
				cancel()
			case "expired deadline":
				var expire context.CancelFunc
				ctx, expire = context.WithDeadline(ctx, time.Unix(0, 0))
				defer expire()
				want = context.DeadlineExceeded
			case "during read":
				go func() {
					<-conn.reading
					cancel()
				}()
			}
			_, err = stream.Read(ctx)
			requireOperationError(t, err, 0, EffectReadOnly)
			if !errors.Is(err, want) {
				t.Fatalf("read cause = %v, want %v", err, want)
			}
			if !conn.closed.Load() {
				t.Fatal("canceled read left attachment open")
			}
			if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("peer did not observe detach: %v", err)
			}
			if _, err := stream.Read(context.Background()); err == nil {
				t.Fatal("read after cancellation succeeded")
			}
		})
	}
}
