package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialObservationDiagnostic(t *testing.T) {
	svc, ws, _, _, out, _ := isolationFixture(t, "codex")
	sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName("codex"))}}, nil, ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopSession(sess.ID)
	var mu sync.Mutex
	var output []byte
	acknowledged := make(chan struct{}, 1)
	unsub, err := svc.Session.Subscribe(sess.ID, func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		output = append(output, data...)
		if strings.Contains(string(output), "ack:ready") {
			select {
			case acknowledged <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if _, err := svc.Session.Take(sess.ID, "web", "test"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Session.Input(sess.ID, "test", []byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	ack := false
	select {
	case <-acknowledged:
		ack = true
	case <-time.After(5 * time.Second):
	}
	snapshot, exists := svc.Session.Snapshot(sess.ID)
	data, readErr := os.ReadFile(out)
	mu.Lock()
	captured := append([]byte(nil), output...)
	mu.Unlock()
	t.Logf("DIAGNOSTIC actualLF ACK=%v processExists=%v process=%s recordExists=%v recordBytes=%d readErr=%v output=%q", ack, exists, snapshot.Process, readErr == nil, len(data), readErr, captured)
	if !ack {
		if err := svc.Session.Input(sess.ID, "test", []byte("\r")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-acknowledged:
			ack = true
		case <-time.After(5 * time.Second):
		}
		mu.Lock()
		captured = append([]byte(nil), output...)
		mu.Unlock()
		t.Logf("DIAGNOSTIC same actual process after nativeEnterCR ACK=%v output=%q", ack, captured)
	}

}
