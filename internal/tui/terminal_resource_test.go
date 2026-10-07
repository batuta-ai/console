package tui

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestTerminalPrintableThroughput(t *testing.T) {
	var s terminalScreen
	s.feed(500, 200, "")
	data := strings.Repeat("a", 1<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	frame := s.feed(500, 200, data)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	t.Logf("bytes=%d duration=%s allocated=%d", len(data), elapsed, after.TotalAlloc-before.TotalAlloc)
	if frame.problem != "" {
		t.Fatal(frame.problem)
	}
	if elapsed > terminalThroughputDeadline {
		t.Fatalf("1 MiB printable input took %s; limit %s", elapsed, terminalThroughputDeadline)
	}
	if !strings.Contains(ansi.Strip(frame.text), strings.Repeat("a", 500)) {
		t.Fatal("missing complete printable row")
	}
}

func TestTerminalMaximumGraphemeStyleHeap(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var s terminalScreen
	s.feed(500, 200, "")
	cluster := "e" + strings.Repeat("\u0301", 30)
	row := strings.Repeat("\x1b[38;2;123;234;255;48;2;231;252;198;1;3;4;9m"+cluster+"\x1b[38;2;255;234;123;48;2;198;252;231m"+cluster, 250)
	start := time.Now()
	for i := 0; i < 600; i++ {
		frame := s.feed(500, 200, row)
		if frame.problem != "" {
			t.Fatal(frame.problem)
		}
	}
	s.feed(500, 200, "\x1b[?1049h")
	for i := 0; i < 200; i++ {
		s.feed(500, 200, row)
	}
	held := s.feed(500, 200, "\x1b[?2026h")
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := after.HeapAlloc - before.HeapAlloc
	t.Logf("retained=%d allocated=%d elapsed=%s held_render_bytes=%d", retained, after.TotalAlloc-before.TotalAlloc, time.Since(start), len(held.text))
	if retained > 128<<20 {
		t.Errorf("owned heap %d exceeds 128 MiB", retained)
	}
	runtime.KeepAlive(&s)
}
