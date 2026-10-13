package service

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialRejectionExistingCLI(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			svc, ws, mission, account, out, _ := isolationFixture(t, adapter)
			path := credentialRejectionExecutable(adapter)
			nativeOriginalCLIInspect(t, path)
			if path != filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(adapter)) {
				t.Fatal("negative-account fixture selected a different executable", path)
			}
			const identity = "rejection-existing-executable"
			authenticateFixtureAccount(t, svc, account, identity)
			args := []string{"space value", "$(literal);*", "ação 日本語", "line1\nline2"}
			argv := append([]string{path}, args...)
			sess, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Argv: argv})
			if err != nil {
				t.Fatal("existing negative-account fixture executable did not support healthy control", err)
			}
			record := readCredentialRecord(t, svc, sess, out)
			if !slices.Equal(sess.Argv, argv) || !slices.Equal(record.Argv, args) || !record.PTY || record.Identity != identity || record.Env["HOME"] != filepath.Join(svc.Home, "accounts", account.ID) {
				t.Fatalf("healthy existing-executable control changed: session=%+v record=%+v", sess, record)
			}
			requireSameDirectory(t, record.CWD, ws.Root)
			if _, err := svc.StopSession(sess.ID); err != nil {
				t.Fatal("healthy existing-executable control did not drain", err)
			}
			t.Logf("actual existing negative-account executable=%s healthy identity=%s PTY=%v literalargv=%q", path, record.Identity, record.PTY, record.Argv)
		})
	}
}
