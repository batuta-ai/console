package service

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialShellSeedAndForwarding(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			expected := credentialRuntimeShell(t)
			file, err := os.Open(expected)
			if err != nil {
				t.Fatal(err)
			}
			var header [2]byte
			_, readErr := io.ReadFull(file, header[:])
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || string(header[:]) != "MZ" {
				t.Fatalf("actual native shell PE metadata=%q read=%v close=%v", header, readErr, closeErr)
			}
			svc, ws, _, _, out, _ := isolationFixture(t, adapter)
			if actual := os.Getenv("SHELL"); actual != expected {
				t.Fatalf("native fixture shell seed=%q expected actual system shell=%q", actual, expected)
			}
			sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(adapter))}}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, svc, sess, out)
			if record.Env["SHELL"] != expected || !record.PTY {
				t.Fatalf("actual native child SHELL=%q expected=%q PTY=%v", record.Env["SHELL"], expected, record.PTY)
			}
			if err := svc.StopSession(sess.ID); err != nil {
				t.Fatal("actual credential child stop", err)
			}
			t.Logf("actual native PE shell fixture seed and child unchanged=%q", expected)
		})
	}
}
