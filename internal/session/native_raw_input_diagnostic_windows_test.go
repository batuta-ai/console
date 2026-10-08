package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestNativeRawInputDiagnosticChild(t *testing.T) {
	if len(os.Args) != 5 || os.Args[3] != "console-raw-input-diagnostic" {
		return
	}
	input := windows.Handle(os.Stdin.Fd())
	var before, output uint32
	if err := windows.GetConsoleMode(input, &before); err != nil {
		t.Fatal(err)
	}
	if err := windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &output); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetConsoleMode(input, windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		t.Fatal(err)
	}
	var after uint32
	if err := windows.GetConsoleMode(input, &after); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RAW-DIAG-READY:%d:%d:%d\n", before, after, output)
	// Go's console File.Read treats Ctrl-Z as EOF even in raw console mode.
	// Read the actual Win32 characters independently of that stream policy.
	data := make([]byte, 0, 6)
	for len(data) < 6 {
		var characters [6]uint16
		var count uint32
		if err := windows.ReadConsole(input, &characters[0], uint32(6-len(data)), &count, nil); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("native console returned no characters")
		}
		for _, character := range characters[:count] {
			if character > 127 {
				t.Fatalf("unexpected non-ASCII diagnostic input U+%04X", character)
			}
			data = append(data, byte(character))
		}
	}
	if err := os.WriteFile(os.Args[4], data, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RAW-DIAG-INPUT:%x\n", data)
	for {
		time.Sleep(time.Hour)
	}
}

func TestNativeRawInputDiagnostic(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			root := t.TempDir()
			record := filepath.Join(root, "actual-input.bin")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			manager := New(root)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			pending, err := manager.StartPending(domain.Session{ID: "raw-diag", Owner: "tui", OwnerClient: "raw-diag-client", Argv: []string{exe, "-test.run=^TestNativeRawInputDiagnosticChild$", "--", "console-raw-input-diagnostic", record}}, testfixture.TerminalEnvironment(t, []string{"HOME=" + root}), root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pending.Persist(func(domain.Session) error { return nil }); err != nil {
				t.Fatal(err)
			}
			sub, err := manager.SubscribeTerminal("raw-diag")
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output strings.Builder
			task43ConsoleReadUntil(t, ctx, sub, &output, "RAW-DIAG-READY:")
			want := []byte{'q', 27, 3, 26, 28, 4}
			if split {
				for _, b := range want {
					if err := manager.Input("raw-diag", "raw-diag-client", []byte{b}); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := manager.Input("raw-diag", "raw-diag-client", want); err != nil {
				t.Fatal(err)
			}
			task43ConsoleReadUntil(t, ctx, sub, &output, "RAW-DIAG-INPUT:")
			got, err := os.ReadFile(record)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("actual native console input=%x want=%x err=%v output=%q", got, want, err, output.String())
			}
			t.Logf("native console trace %q; independent input=%x", output.String(), got)
		})
	}
}
