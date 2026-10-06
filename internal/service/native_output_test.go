package service

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestNativeOutputAttachesOpeningOnceBeforeStream(t *testing.T) {
	var active atomic.Bool
	calls := 0
	writer := &nativeOpeningWriter{ctx: context.Background(), active: &active, guard: func(context.Context) error { calls++; return nil }}
	if _, err := writer.Write([]byte("preview")); err != nil || calls != 0 {
		t.Fatal("pre-run output attempted attachment", calls, err)
	}
	active.Store(true)
	for i := 0; i < 100; i++ {
		if n, err := writer.Write([]byte("progress")); n != 8 || err != nil {
			t.Fatal(n, err)
		}
	}
	if calls != 1 {
		t.Fatal("stream output repeatedly re-read the journal instead of using its durable attachment", calls)
	}
}
