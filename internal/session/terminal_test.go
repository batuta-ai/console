//go:build darwin || linux

package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestDetachReconnectAndNativeResize(t *testing.T) {
	m := New(t.TempDir())
	p, err := m.StartPending(domain.Session{ID: "terminal", Argv: []string{"/bin/sh", "-c", `printf 'READY\n'; while read line; do stty size; printf 'ACK:%s\n' "$line"; done`}}, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("terminal")
	output := make(chan string, 64)
	unsub, err := m.Subscribe("terminal", func(b []byte) { output <- string(b) })
	if err != nil {
		t.Fatal(err)
	}
	var got string
	await := func(want string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			if strings.Contains(got, want) {
				return
			}
			select {
			case s := <-output:
				got += s
				if strings.Contains(got, want) {
					return
				}
			case <-timer.C:
				t.Fatalf("missing %q: %q", want, got)
			}
		}
	}
	await("READY")
	unsub()
	pid := p.Snapshot().PID
	obs, err := InspectProcess(pid)
	if err != nil || obs.Missing {
		t.Fatalf("detach stopped leader: %+v %v", obs, err)
	}
	unsub, err = m.Subscribe("terminal", func(b []byte) { output <- string(b) })
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if _, err := m.Take("terminal", "web", "second"); err != nil {
		t.Fatal(err)
	}
	if err := m.Resize("terminal", "foreign", 97, 31); !os.IsPermission(err) {
		t.Fatalf("foreign resize: %v", err)
	}
	if err := m.Input("terminal", "foreign", []byte("wrong\n")); !os.IsPermission(err) {
		t.Fatalf("foreign input: %v", err)
	}
	if err := m.Resize("terminal", "second", 97, 31); err != nil {
		t.Fatal(err)
	}
	if err := m.Input("terminal", "second", []byte("continued\n")); err != nil {
		t.Fatal(err)
	}
	await("31 97")
	await("ACK:continued")
	s := p.Snapshot()
	if s.PID != pid || s.Process != "running" || s.Cols != 97 || s.Rows != 31 {
		t.Fatalf("resize changed lifecycle: %+v", s)
	}
	if _, err := m.Stop("terminal"); err != nil {
		t.Fatalf("terminal cleanup: %v", err)
	}
}
