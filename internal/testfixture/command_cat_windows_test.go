package testfixture_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
)

func TestNativeCatFixtureActualFiniteInput(t *testing.T) {
	root := testfixture.PrivateHome(t)
	runner, _ := scriptNativeRunner(t, root)
	argv := testfixture.Argv(t, []string{"/bin/cat"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := append([]byte{0, 27, 26}, []byte("literal ação 日本語\nsecond line\n")...)
	result, err := runner.Run(ctx, publication.Command{Executable: argv[0], Directory: root, Stdin: input})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, input) || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("actual native cat finite stdin/stdout/exit differs", result, err)
	}
	image, err := os.ReadFile(argv[0])
	if err != nil || len(image) < 2 || string(image[:2]) != "MZ" || !filepath.IsAbs(argv[0]) || !strings.EqualFold(filepath.Ext(argv[0]), ".exe") {
		t.Fatal("actual native cat PE/path metadata missing", argv, err)
	}
	t.Logf("actual installed native cat=%s imageSHA=%x finitebytes=%d", argv[0], sha256.Sum256(image), len(input))
}

func TestNativeCatFixtureActualTerminalAndOwnedStop(t *testing.T) {
	root := testfixture.PrivateHome(t)
	runner, environment := scriptNativeRunner(t, root)
	argv := testfixture.Argv(t, []string{"/bin/cat", "-n"})
	if len(argv) != 2 || argv[1] != "-n" {
		t.Fatal("original cat argument changed", argv)
	}
	pending, err := runner.Manager.StartPending(domain.Session{ID: "actual-native-cat", Argv: argv, Cols: 80, Rows: 24, Owner: "tui", OwnerClient: "cat-client"}, environment, root, 1)
	if err != nil {
		t.Fatal("original cat fixture actual native start", err)
	}
	sub, err := runner.Manager.SubscribeTerminal("actual-native-cat")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.Manager.InputContext(ctx, "actual-native-cat", "cat-client", []byte("CAT-ACTUAL-PROGRAM-OUTPUT\r")); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	ack := regexp.MustCompile(`1[ \t]+CAT-ACTUAL-PROGRAM-OUTPUT`)
	for !ack.MatchString(output.String()) {
		event, err := sub.Next(ctx)
		output.Write(event.Data)
		if err != nil {
			t.Fatal("actual cat numbered output missing", output.String(), err)
		}
	}
	if pending.Snapshot().Process != "running" || pending.Snapshot().EndedAt != nil {
		t.Fatal("actual native cat is not live", pending.Snapshot())
	}
	if _, err := runner.Manager.Stop("actual-native-cat"); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := sub.Next(ctx)
		output.Write(event.Data)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("actual native cat did not join", err)
		}
	}
	if pending.Snapshot().Process != "exited" || pending.Snapshot().EndedAt == nil {
		t.Fatal("actual native cat stop lifetime missing", pending.Snapshot())
	}
	t.Logf("actual native cat program numbered output and owned stop/join: %q", output.String())
}

func TestNativeCatFixtureKeepsOtherOriginalArguments(t *testing.T) {
	for _, original := range [][]string{nil, {}, {"unknown"}, {"/bin/unknown", "literal ação", "$(literal)"}, {"relative.exe", "two\nlines"}} {
		if got := testfixture.Argv(t, original); !reflect.DeepEqual(got, original) {
			t.Fatal("unrelated original argv changed", got, original)
		}
	}
}
