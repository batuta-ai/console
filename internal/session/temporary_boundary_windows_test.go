package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsOwnedCommandTemporaryBoundary(t *testing.T) {
	for _, kind := range []string{"long", "missing", "foreign-writable"} {
		t.Run(kind, func(t *testing.T) {
			cwd := task43SessionHome(t)
			env, home, err := accounts.SessionEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			target := filepath.Join(home, "temporary-boundary")
			if kind == "long" {
				for len(utf16.Encode([]rune(target))) <= 280 {
					target = filepath.Join(target, "long-temporary-component-"+strings.Repeat("x", 20))
				}
			}
			if kind == "foreign-writable" {
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;0x40;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				name, err := windows.UTF16PtrFromString(target)
				if err != nil {
					t.Fatal(err)
				}
				sa := windows.SecurityAttributes{SecurityDescriptor: sd}
				sa.Length = uint32(unsafe.Sizeof(sa))
				if err := windows.CreateDirectory(name, &sa); err != nil {
					t.Fatal(err)
				}
			} else if err := consoleruntime.EnsurePrivateDirectory(target); err != nil {
				t.Fatal(err)
			}
			var selected []string
			for _, entry := range env {
				key, _, _ := strings.Cut(entry, "=")
				if key == "TEMP" || key == "TMP" {
					if kind == "missing" {
						continue
					}
					entry = key + "=" + target
				}
				selected = append(selected, entry)
			}
			argv := task43ProbeArgv(t, "hold")
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir, cmd.Env = cwd, accounts.RuntimeEnvironment(os.Environ(), selected)
			owned, err := startOwnedCommand(cmd)
			if owned != nil {
				if stopErr := owned.stop(); stopErr != nil {
					t.Error(stopErr)
				}
				if cleanup, wait := waitOwnedOutcome(cmd, owned); cleanup != nil || wait != nil {
					t.Error(cleanup, wait)
				}
			}
			if err == nil || owned != nil || cmd.Process != nil {
				t.Errorf("unsafe temporary metadata launched a process: error=%v process=%v", err, cmd.Process)
			}
		})
	}
}
