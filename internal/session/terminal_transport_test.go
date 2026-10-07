//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestTerminalOrderedReplayAndLimits(t *testing.T) {
	m := New(t.TempDir())
	l := &live{sess: domain.Session{ID: "s", Cols: 80, Rows: 24}, replay: bytes.Repeat([]byte("r"), 64<<10)}
	m.live["s"] = l
	sub, err := m.SubscribeTerminal("s")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	l.mu.Lock()
	l.publishTerminal(TerminalEvent{Data: []byte("live")})
	l.publishTerminal(TerminalEvent{Cols: 97, Rows: 31})
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := sub.Next(ctx)
	if err != nil || e.Cols != 80 || e.Rows != 24 {
		t.Fatalf("initial: %+v %v", e, err)
	}
	var replay []byte
	for len(replay) < 64<<10 {
		e, err = sub.Next(ctx)
		if err != nil || !e.Replay || len(e.Data) > 4096 {
			t.Fatalf("replay: %+v %v", e, err)
		}
		replay = append(replay, e.Data...)
	}
	if !bytes.Equal(replay, l.replay) {
		t.Fatal("replay loss")
	}
	e, err = sub.Next(ctx)
	if err != nil || string(e.Data) != "live" || e.Replay {
		t.Fatalf("live: %+v %v", e, err)
	}
	e, err = sub.Next(ctx)
	if err != nil || e.Cols != 97 || e.Rows != 31 {
		t.Fatalf("resize: %+v %v", e, err)
	}
	for _, kind := range []string{"bytes", "events"} {
		t.Run(kind, func(t *testing.T) {
			q := newTerminalSubscription(nil)
			for i := 0; i < 256; i++ {
				e := TerminalEvent{Cols: 80, Rows: 24}
				if kind == "bytes" {
					e = TerminalEvent{Data: bytes.Repeat([]byte("x"), 4096)}
				}
				q.enqueue(e)
			}
			if q.err != nil {
				t.Fatal(q.err)
			}
			q.enqueue(TerminalEvent{Data: []byte("x")})
			if _, err := q.Next(ctx); !errors.Is(err, ErrTerminalOutputOverflow) {
				t.Fatalf("overflow: %v", err)
			}
		})
	}
}

func TestTerminalTinyFramesAndIndependentQueueBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q := newTerminalSubscription(nil)
	for i := 0; i < 4096; i++ {
		q.enqueue(TerminalEvent{Data: []byte{byte(i % 251)}, Replay: true})
	}
	q.enqueue(TerminalEvent{Data: []byte("LIVE")})
	q.enqueue(TerminalEvent{Cols: 97, Rows: 31})
	q.enqueue(TerminalEvent{Data: []byte("AFTER")})
	if len(q.events) != 4 || q.bytes != 4105 {
		t.Fatalf("merge crossed barrier: %d events %d bytes", len(q.events), q.bytes)
	}
	e, err := q.Next(ctx)
	if err != nil || len(e.Data) != 4096 || !e.Replay {
		t.Fatalf("merged replay %+v %v", e, err)
	}
	for i, b := range e.Data {
		if b != byte(i%251) {
			t.Fatalf("tiny output corruption at %d", i)
		}
	}
	for _, want := range []TerminalEvent{{Data: []byte("LIVE")}, {Cols: 97, Rows: 31}, {Data: []byte("AFTER")}} {
		e, err = q.Next(ctx)
		if err != nil || !bytes.Equal(e.Data, want.Data) || e.Cols != want.Cols || e.Rows != want.Rows || e.Replay {
			t.Fatalf("barrier %+v want %+v: %v", e, want, err)
		}
	}
	// The real PTY emits <=4096-byte events: its byte ceiling coincides with
	// the event ceiling for full chunks. Exercise the byte guard independently
	// here using larger events, without calling this a WebSocket proof.
	q = newTerminalSubscription(nil)
	for i := 0; i < 16; i++ {
		q.enqueue(TerminalEvent{Data: make([]byte, 64<<10)})
	}
	if len(q.events) != 16 || q.bytes != 1<<20 || q.err != nil {
		t.Fatalf("byte limit boundary: %+v", q)
	}
	q.enqueue(TerminalEvent{Data: []byte{1}})
	if !errors.Is(q.err, ErrTerminalOutputOverflow) || q.bytes != 0 || len(q.events) != 0 {
		t.Fatal("byte overflow retained queue")
	}
	select {
	case <-q.Failed():
	default:
		t.Fatal("missing visible byte overflow")
	}
	q = newTerminalSubscription(nil)
	for i := 0; i < 256; i++ {
		q.enqueue(TerminalEvent{Data: []byte{1}})
		q.enqueue(TerminalEvent{Cols: 80, Rows: 24})
		if q.err != nil {
			break
		}
	}
	if !errors.Is(q.err, ErrTerminalOutputOverflow) {
		t.Fatal("tiny events failed to overflow")
	}
	select {
	case <-q.Failed():
	default:
		t.Fatal("missing visible event overflow")
	}
}
