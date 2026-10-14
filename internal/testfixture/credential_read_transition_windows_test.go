package testfixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialWorldReadablePrivacyTransition(t *testing.T) {
	path := newCredentialReadFile(t)
	f, err := consolert.OpenPrivateFile(path)
	if err != nil {
		t.Fatal("positive private credential prerequisite", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.MakeCredentialWorldReadable(path); err != nil {
		t.Fatal(err)
	}
	f, err = consolert.OpenPrivateFile(path)
	if f != nil {
		if closeErr := f.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "private Windows DACL must grant only the current user") {
		t.Fatal("World-readable credential did not fail for its actual DACL", err)
	}
	t.Log("actual same credential accepted before DACL mutation and rejected for World-readable DACL afterward")
}

func TestNativeCredentialWorldReadableErrorCauses(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			target := newCredentialReadFile(t)
			path := filepath.Join(filepath.Dir(target), "candidate")
			want := os.ErrInvalid
			switch kind {
			case "missing":
				want = os.ErrNotExist
			case "directory":
				if err := testfixture.MakePrivateDirectory(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := testfixture.MakeCredentialWorldReadable(path); !errors.Is(err, want) {
				t.Fatalf("actual refusal lost its error cause: kind=%s error=%v want=%v", kind, err, want)
			}
		})
	}
}
