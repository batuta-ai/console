package testfixture_test

import (
	"context"
	"debug/pe"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
)

func nativeUtilityArgv(t *testing.T, original []string) []string {
	t.Helper()
	before := append([]string(nil), original...)
	argv := testfixture.Argv(t, original)
	if len(argv) != len(before) || !reflect.DeepEqual(argv[1:], before[1:]) || !reflect.DeepEqual(original, before) {
		t.Fatalf("utility argv bytes or original changed: got=%q original=%q want=%q", argv, original, before)
	}
	want := strings.TrimPrefix(filepath.Base(before[0]), "/") + ".exe"
	if argv[0] == before[0] || !filepath.IsAbs(argv[0]) || !strings.EqualFold(filepath.Base(argv[0]), want) {
		t.Errorf("original Unix utility did not resolve to absolute native %s: %q", want, argv[0])
	}
	image, err := pe.Open(argv[0])
	if err != nil {
		t.Errorf("resolved utility is not an actual PE executable: path=%q error=%v", argv[0], err)
	} else {
		if image.FileHeader.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || image.FileHeader.Characteristics&pe.IMAGE_FILE_DLL != 0 {
			t.Errorf("resolved utility is not a PE program: path=%q characteristics=%#x", argv[0], image.FileHeader.Characteristics)
		}
		if err := image.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("original utility argv=%q resolved argv=%q", before, argv)
	return argv
}

func TestNativeUtilityFixturesActualBehavior(t *testing.T) {
	for _, test := range []struct {
		name    string
		argv    []string
		minimum time.Duration
		invalid bool
	}{
		{name: "true", argv: []string{"/usr/bin/true"}},
		{name: "true_literal_arguments", argv: []string{"/usr/bin/true", "", " space value ", "quote\"'", "$(literal);*", "ação 日本語", "line1\nline2", `C:\literal\path\`}},
		{name: "fractional_sleep", argv: []string{"/bin/sleep", "0.25", "0"}, minimum: 250 * time.Millisecond},
		{name: "sleep_invalid_literal_operand", argv: []string{"/bin/sleep", "0.25", "0.25 0.25"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			runner, _ := scriptNativeRunner(t, root)
			argv := nativeUtilityArgv(t, test.argv)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			start := time.Now()
			result, err := runner.Run(ctx, publication.Command{Executable: argv[0], Args: argv[1:], Directory: root})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("actual native utility execution: argv=%q error=%v", argv, err)
			}
			if result.StdoutTruncated || result.StderrTruncated || len(result.Stdout) != 0 {
				t.Fatalf("utility output differs: %+v", result)
			}
			if test.invalid {
				if result.ExitCode == 0 || len(result.Stderr) == 0 {
					t.Fatalf("sleep accepted an unsplit invalid operand: %+v", result)
				}
			} else if result.ExitCode != 0 || len(result.Stderr) != 0 || elapsed < test.minimum {
				t.Fatalf("utility behavior differs: result=%+v elapsed=%v minimum=%v", result, elapsed, test.minimum)
			}
			t.Logf("actual native utility exit=%d stdout=%q stderr=%q elapsed=%v", result.ExitCode, result.Stdout, result.Stderr, elapsed)
		})
	}
}

func TestNativeUtilitySleepOwnedCancellation(t *testing.T) {
	root := testfixture.PrivateHome(t)
	runner, environment := scriptNativeRunner(t, root)
	argv := nativeUtilityArgv(t, []string{"/bin/sleep", "30"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := runner.Manager.Probe(ctx, argv, environment, root)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Output) != 0 || elapsed > 2*time.Second {
		t.Fatalf("actual sleep cancellation not bounded: result=%+v error=%v elapsed=%v", result, err, elapsed)
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDrain()
	if err := runner.Manager.Shutdown(drain); err != nil {
		t.Fatal("owned sleep cleanup did not drain", err)
	}
	t.Logf("actual native sleep cancellation=%v elapsed=%v owned shutdown drained", err, elapsed)
}
