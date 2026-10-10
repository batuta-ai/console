package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialShellMechanismDiagnostic(t *testing.T) {
	for _, conversion := range []string{"default", "exclude-SHELL"} {
		t.Run(conversion, func(t *testing.T) {
			svc, ws, _, _, out, root := isolationFixture(t, "codex")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			inner := filepath.Join(root, "inside-shell-value.txt")
			script := "#!/bin/sh\nprintf '%s' \"$SHELL\" > " + quote(inner) + "\n"
			if conversion == "exclude-SHELL" {
				script += "export MSYS2_ENV_CONV_EXCL='SHELL'\n"
			}
			script += "exec " + quote(executable) + " -test.run '^TestCredentialRecordChild$' -- console-credential-fixture " + quote(out) + " " + quote(root) + " 'codex'\n"
			path := filepath.Join(root, "bin", "shell-observation.exe")
			if err := testfixture.WriteScript(t, path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{path}}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.StopSession(sess.ID)
			record := readCredentialRecord(t, svc, sess, out)
			value, err := os.ReadFile(inner)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("DIAGNOSTIC conversion=%s seededSHELL=%q insideActualShell=%q actualNativeChildSHELL=%q", conversion, os.Getenv("SHELL"), value, record.Env["SHELL"])
		})
	}
}
