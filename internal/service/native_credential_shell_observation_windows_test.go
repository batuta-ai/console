package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialShellObservationDiagnostic(t *testing.T) {
	for _, transport := range []string{"msys-wrapper", "direct-native-control"} {
		t.Run(transport, func(t *testing.T) {
			svc, ws, _, _, out, root := isolationFixture(t, "codex")
			argv := []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName("codex"))}
			if transport == "direct-native-control" {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				argv = []string{executable, "-test.run=^TestCredentialRecordChild$", "--", "console-credential-fixture", out, root, "codex"}
			}
			sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: argv}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.StopSession(sess.ID)
			record := readCredentialRecord(t, svc, sess, out)
			t.Logf("DIAGNOSTIC transport=%s seededSHELL=%q actualChildSHELL=%q", transport, os.Getenv("SHELL"), record.Env["SHELL"])
		})
	}
}
