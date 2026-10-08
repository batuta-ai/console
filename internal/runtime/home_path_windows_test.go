package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTask43WindowsRuntimeRejectsAmbiguousHome(t *testing.T) {
	for _, kind := range []string{"relative", "drive-relative", "device", "UNC"} {
		t.Run(kind, func(t *testing.T) {
			base := task43PrivateHome(t)
			t.Chdir(base)
			path := "relative"
			switch kind {
			case "drive-relative":
				path = filepath.VolumeName(base) + "relative"
			case "device":
				path = `\\?\` + base
			case "UNC":
				path = `\\localhost\C$\task43-invalid`
			}
			if paths, err := Resolve(path, "127.0.0.1:0"); paths != (Paths{}) || err == nil {
				t.Fatalf("ambiguous home accepted: %+v error=%v", paths, err)
			}
			if _, err := os.Lstat(filepath.Join(base, "relative")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ambiguous path created private artifacts", err)
			}
		})
	}
}
