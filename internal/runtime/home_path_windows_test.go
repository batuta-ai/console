package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTask43WindowsRuntimeRejectsAmbiguousHome(t *testing.T) {
	base := task43PrivateHome(t)
	t.Chdir(base)
	volume := filepath.VolumeName(base)
	for _, path := range []string{"relative", volume + "relative", `\\?\` + base, `\\localhost\C$\task43-invalid`} {
		t.Run(path, func(t *testing.T) {
			if paths, err := Resolve(path, "127.0.0.1:0"); paths != (Paths{}) || err == nil {
				t.Fatalf("ambiguous home accepted: %+v error=%v", paths, err)
			}
			if _, err := os.Lstat(filepath.Join(base, "relative")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ambiguous path created private artifacts", err)
			}
		})
	}
}
