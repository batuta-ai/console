package session

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Preserve each original fixture's behavior using a native console process.
// Unix keeps the original executable and argv byte-for-byte.
func sessionTestArgv(t *testing.T, original []string) []string {
	t.Helper()
	if len(original) == 0 {
		t.Fatal("empty original session fixture")
	}
	mode := ""
	args := original[1:]
	switch original[0] {
	case "/bin/cat":
		mode = "cat"
	case "/bin/sleep":
		mode = "sleep"
	case "/bin/ls":
		mode = "ls"
	case "/usr/bin/true":
		mode = "true"
	case "/bin/sh":
		if len(args) != 2 || args[0] != "-c" || args[1] != "printf hi; read answer" {
			t.Fatal("unmapped original session shell fixture", original)
		}
		mode, args = "replay", nil
	default:
		t.Fatal("unmapped original session fixture", original)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{exe, "-test.run=^TestSessionNativeFixture$", "--", "console-session-fixture", mode}, args...)
}

func TestSessionNativeFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "console-session-fixture" {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode); err != nil {
		t.Fatal("original session fixture is not a native console", err)
	}
	switch os.Args[4] {
	case "cat":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			t.Fatal(err)
		}
	case "sleep":
		if len(os.Args) != 6 {
			t.Fatal("missing fixture duration")
		}
		seconds, err := strconv.ParseFloat(os.Args[5], 64)
		if err != nil || seconds < 0 {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(seconds * float64(time.Second)))
	case "ls":
		for _, path := range os.Args[5:] {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("original fixture workspace marker", err)
			}
			fmt.Fprintln(os.Stdout, path)
		}
	case "true":
	case "replay":
		fmt.Fprint(os.Stdout, "hi")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() && scanner.Err() != nil {
			t.Fatal(scanner.Err())
		}
	default:
		t.Fatal("unknown native session fixture")
	}
	os.Exit(0)
}
