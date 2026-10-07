//go:build darwin || linux

package tui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestTerminalNativeActorMaximumHeap(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	m, manager := terminalNativeAttachment(t, nil)
	m = terminalNativeTake(t, m)
	m.sendResize(500, 200)
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.cols == 500 && f.rows == 200 })
	m.width, m.height = 500, 210
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	terminalNativeOutput(t, ctx, sub, []byte("READY"))
	cluster := "e" + strings.Repeat("\u0301", 30)
	row := strings.Repeat("\x1b[38;2;123;234;255;48;2;231;252;198;1;3;4;9m"+cluster+"\x1b[38;2;255;234;123;48;2;198;252;231m"+cluster, 250)
	send := func(data string, childRow int) {
		t.Helper()
		next, _ := m.Update(tea.PasteMsg{Content: data + "\x1b[6n"})
		m = next.(model)
		// The real parser reply returns through the outgoing binary queue,
		// child PTY and native HTTP/WebSocket. It follows the full row feed.
		replyCtx, stopReply := context.WithTimeout(ctx, 5*time.Second)
		defer stopReply()
		terminalNativeOutput(t, replyCtx, sub, []byte(fmt.Sprintf("\x1b[%d;500R", childRow)))
	}
	for i := 0; i < 600; i++ {
		data := row
		if i == 0 {
			data = "\x1b[2J\x1b[H" + data
		}
		send(data, min(i+1, 200))
	}
	for i := 0; i < 200; i++ {
		data := row
		if i == 0 {
			data = "\x1b[?1049h" + data
		}
		send(data, i+1)
	}
	next, _ := m.Update(tea.PasteMsg{Content: "\x1b[?2026h"})
	m = next.(model)
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.held })
	view := m.View().Content
	if !strings.Contains(view, "synchronized output held") {
		t.Fatal("actual View did not retain held frame")
	}
	// Include full bounded queue representations in the same retained-heap
	// measurement. The active actor itself is paced by real parser replies;
	// these independent mailboxes account conservatively for both 1 MiB
	// payload bounds without claiming a saturated native flow here.
	output := newTerminalMailbox[terminalAction]()
	for i := 0; i < 256; i++ {
		a := terminalAction{}
		a.event.Data = make([]byte, 4096)
		if !output.push(a, len(a.event.Data)) {
			t.Fatal("exact output queue bound rejected")
		}
	}
	writes := newTerminalMailbox[terminalWrite]()
	if !writes.push(terminalWrite{data: make([]byte, 1<<20)}, 1<<20) {
		t.Fatal("exact input queue bound rejected")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := after.HeapAlloc - before.HeapAlloc
	t.Logf("actual native actor retained=%d allocated=%d elapsed=%s held_frame=%d view=%d full_queue_payload=%d", retained, after.TotalAlloc-before.TotalAlloc, time.Since(start), len(m.termFrame.text), len(view), 2<<20)
	if retained > 128<<20 {
		t.Errorf("owned actor, View and bounded queue heap %d exceeds 128 MiB", retained)
	}
	runtime.KeepAlive(m)
	runtime.KeepAlive(view)
	runtime.KeepAlive(output)
	runtime.KeepAlive(writes)
}
