package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialLiteralLFAndNaturalExit(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			svc, ws, _, _, out, _ := isolationFixture(t, adapter)
			exited := make(chan domain.Session, 1)
			previous := svc.Session.OnExit
			svc.Session.OnExit = func(s domain.Session) {
				if previous != nil {
					previous(s)
				}
				exited <- s
			}
			sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(adapter))}}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := svc.Session.SubscribeTerminal(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			readyProcess(t, svc, sess)
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var record credentialRecord
			if err := json.Unmarshal(data, &record); err != nil || !record.PTY {
				t.Fatalf("actual native credential child record: PTY=%v err=%v", record.PTY, err)
			}
			if current, exists := svc.Session.Snapshot(sess.ID); !exists || current.Process != "running" {
				t.Fatalf("acknowledged native child is not running: %+v exists=%v", current, exists)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := svc.Session.Input(sess.ID, "test", []byte("exit\n")); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for {
				event, err := sub.Next(ctx)
				output.Write(event.Data)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("literal exit LF did not naturally drain: %v; output=%q", err, output.String())
				}
			}
			if !strings.Contains(output.String(), "ack:ready") || strings.Contains(output.String(), "ack:exit") {
				t.Fatalf("real child LF protocol changed: %q", output.String())
			}
			select {
			case actual := <-exited:
				if actual.ID != sess.ID || actual.Process != "exited" || actual.EndedAt == nil {
					t.Fatalf("native natural exit observation: %+v", actual)
				}
			case <-ctx.Done():
				t.Fatal("native child exit callback missing", ctx.Err())
			}
			if err := svc.Session.Shutdown(ctx); err != nil {
				t.Fatal("native child retirement failed", err)
			}
		})
	}
}
