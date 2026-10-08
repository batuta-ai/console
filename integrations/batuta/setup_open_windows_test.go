//go:build windows

package batuta

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestTask43WindowsSetupReader(t *testing.T) {
	for _, kind := range []string{"regular", "native", "limit", "oversize", "directory", "hardlink", "file_symlink", "parent_symlink", "parent_file", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, ".batuta")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "profile.md")
			payload := []byte("exact caller bytes\r\n")
			if kind == "limit" || kind == "oversize" {
				size := SetupFileLimit
				if kind == "oversize" {
					size++
				}
				payload = bytes.Repeat([]byte("x"), size)
			}
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "missing":
			case "parent_file":
				if err = os.Remove(parent); err == nil {
					err = os.WriteFile(parent, payload, 0600)
				}
			case "parent_symlink":
				target := t.TempDir()
				if err = os.WriteFile(filepath.Join(target, "profile.md"), payload, 0600); err == nil {
					if err = os.Remove(parent); err == nil {
						err = os.Symlink(target, parent)
					}
				}
			case "file_symlink", "hardlink":
				target := filepath.Join(dir, "target")
				if err = os.WriteFile(target, payload, 0600); err == nil {
					if kind == "hardlink" {
						err = os.Link(target, path)
					} else {
						err = os.Symlink(target, path)
					}
				}
			default:
				err = os.WriteFile(path, payload, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			rel := ".batuta/profile.md"
			if kind == "native" {
				rel = filepath.Join(".batuta", "profile.md")
			}
			got, err := readSetupFile(root, rel)
			if kind == "regular" || kind == "native" || kind == "limit" {
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("bounded exact regular read: bytes=%d error=%v", len(got), err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("unsafe file accepted: kind=%s bytes=%d error=%v", kind, len(got), err)
			}
			if kind == "missing" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing file identity: %v", err)
			}
		})
	}
}

func TestTask43WindowsSetupReaderRejectsNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "WORK.md"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, path := range []string{"", ".", "..", "../WORK.md", `..\WORK.md`, "./WORK.md", ".//WORK.md", `a\\WORK.md`, "/WORK.md", `\WORK.md`, `C:WORK.md`, filepath.Join(dir, "WORK.md"), `\\.\pipe\batuta-task43`, "WORK.md:stream", "WORK.md.", "WORK.md ", "NUL", "CON", "COM1", "LPT1", "WORK.md\x00"} {
		t.Run(path, func(t *testing.T) {
			f, err := openSetupFile(root, path)
			if f != nil {
				f.Close()
			}
			if f != nil || !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("invalid Windows name: file=%v error=%v", f, err)
			}
		})
	}
}

func TestTask43WindowsSetupReaderAnchoredRoot(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	want := []byte("original selected root\r\n")
	if err := os.WriteFile(filepath.Join(workspace, "WORK.md"), want, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(workspace, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "WORK.md"), []byte("foreign replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openSetupFile(root, "WORK.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("root was rebound: bytes=%q error=%v", got, err)
	}
}
