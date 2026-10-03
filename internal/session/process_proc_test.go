package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func procStat(pid int, name, state, start string) string {
	return fmt.Sprintf("%d (%s) %s %s %s 0 0", pid, name, state, strings.Repeat("0 ", 18), start)
}

func TestLinuxProcBirthParsing(t *testing.T) {
	boot := "12345678-abcd-1234-abcd-123456789abc"
	for _, name := range []string{"simple", "name with spaces", "nested (parens) and ) end"} {
		a, err := parseProcIdentity(42, []byte(procStat(42, name, "S", "12345")), []byte(boot+"\n"))
		if err != nil || a.Identity == "" || a.Missing {
			t.Fatalf("%q: %+v %v", name, a, err)
		}
		for _, change := range []struct{ tick, boot string }{{"12346", boot}, {"12345", "87654321-abcd-1234-abcd-123456789abc"}} {
			b, err := parseProcIdentity(42, []byte(procStat(42, name, "R", change.tick)), []byte(change.boot))
			if err != nil || a.Identity == b.Identity {
				t.Fatalf("reused birth/boot matched: %+v %+v %v", a, b, err)
			}
		}
	}
	for _, state := range []string{"Z", "X", "x"} {
		got, err := parseProcIdentity(42, []byte(procStat(42, "dead", state, "0")), nil)
		if err != nil || !got.Missing || got.Identity != "" {
			t.Fatalf("zombie/dead: %+v %v", got, err)
		}
	}
	for _, tc := range []struct{ stat, boot string }{
		{"", boot}, {"42 (broken", boot}, {"42 (short) S 0", boot},
		{procStat(41, "reused", "S", "12345"), boot},
		{procStat(42, "bad", "?", "12345"), boot},
		{procStat(42, "bad", "S", "-1"), boot},
		{procStat(42, "bad", "S", "no-tick"), boot},
		{procStat(42, "bad", "S", "18446744073709551616"), boot},
		{procStat(42, "bad", "S", "12345"), ""},
		{procStat(42, "bad", "S", "12345"), "malformed-boot"},
	} {
		if got, err := parseProcIdentity(42, []byte(tc.stat), []byte(tc.boot)); err == nil || got.Missing || got.Identity != "" {
			t.Errorf("malformed observation guessed: %+v %v", got, err)
		}
	}
}

func TestLinuxProcReadErrors(t *testing.T) {
	root := t.TempDir()
	// Visible proc mount, no PID directory: absence does not need boot identity.
	if err := os.WriteFile(filepath.Join(root, "mounts"), []byte("proc /proc proc rw 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := inspectProc(root, 42)
	if err != nil || !got.Missing {
		t.Fatalf("missing: %+v %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(root, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "42", "stat"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err = inspectProc(root, 42)
	if err == nil || got.Missing {
		t.Fatalf("unreadable stat classified missing: %+v %v", got, err)
	}
	if err := os.Remove(filepath.Join(root, "42", "stat")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "42", "stat"), []byte(procStat(42, "live", "S", "123")), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = inspectProc(root, 42)
	if err == nil || got.Missing {
		t.Fatalf("missing boot guessed absence: %+v %v", got, err)
	}
}

func TestLinuxProcUnavailableIsNotMissing(t *testing.T) {
	got, err := inspectProc(filepath.Join(t.TempDir(), "unmounted"), 42)
	if err == nil || got.Missing {
		t.Fatalf("unavailable proc guessed absence: %+v %v", got, err)
	}
}

func TestLinuxProcHiddenPIDIsUnverified(t *testing.T) {
	for _, options := range []string{"rw,hidepid=2", "rw,hidepid=invisible", "rw,hidepid=4", "rw,hidepid=ptraceable", "rw,hidepid=noaccess"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "mounts"), []byte("proc /proc proc "+options+" 0 0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := inspectProc(root, 42)
		if err == nil || got.Missing {
			t.Errorf("restricted visibility guessed absent (%s): %+v %v", options, got, err)
		}
	}
	for _, record := range []string{"", "malformed", "tmpfs /proc tmpfs rw 0 0", "proc /other proc rw 0 0"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "mounts"), []byte(record), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := inspectProc(root, 42)
		if err == nil || got.Missing {
			t.Errorf("invalid mount observation guessed absent: %+v %v", got, err)
		}
	}
	for _, options := range []string{"rw,relatime", "rw,hidepid=0", "rw,hidepid=off"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "mounts"), []byte("proc /proc proc "+options+" 0 0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := inspectProc(root, 42)
		if err != nil || !got.Missing {
			t.Errorf("visible absent process: %+v %v", got, err)
		}
	}
}
