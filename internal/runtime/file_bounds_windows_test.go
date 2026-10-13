package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

type task43BoundFileID struct{ volume, high, low uint32 }

func task43BoundIdentity(t *testing.T, path string) task43BoundFileID {
	t.Helper()
	f, err := OpenPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	queryErr := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
	closeErr := f.Close()
	if queryErr != nil || closeErr != nil {
		t.Fatal(queryErr, closeErr)
	}
	return task43BoundFileID{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}
}

func TestTask43WindowsAuthSecretSizeBounds(t *testing.T) {
	for _, size := range []int{31, 32, (64 << 10) - 1, 64 << 10, (64 << 10) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(task43PrivateHome(t), "auth.secret")
			want := []byte(strings.Repeat("s", size))
			f, err := CreatePrivateFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.Write(want)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatal(writeErr, closeErr)
			}
			before := task43BoundIdentity(t, path)
			token, err := LoadOrCreateAuth(path)
			if size < 32 || size > 64<<10 {
				if err == nil || token != "" {
					t.Errorf("out-of-bound secret returned token length %d, error %v", len(token), err)
				}
			} else if err != nil || token != string(want) {
				t.Errorf("in-bound secret was not returned completely: length %d error %v", len(token), err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, want) {
				t.Fatal("secret bytes changed", err)
			}
			if task43BoundIdentity(t, path) != before {
				t.Fatal("secret identity changed")
			}
		})
	}
}

func task43BoundLockInfo(t *testing.T, size int) LockInfo {
	t.Helper()
	info := LockInfo{PID: 123, Addr: "127.0.0.1:9477"}
	base, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	info.Home = strings.Repeat("h", size-len(base))
	encoded, err := json.Marshal(info)
	if err != nil || len(encoded) != size {
		t.Fatalf("fixture encoded size %d, want %d: %v", len(encoded), size, err)
	}
	return info
}

func TestTask43WindowsLockMetadataWriteSizeBounds(t *testing.T) {
	for _, size := range []int{windowsMaxLockBytes - 1, windowsMaxLockBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(task43PrivateHome(t), "consoled.lock")
			want := task43BoundLockInfo(t, size)
			f, err := WriteLock(path, want)
			if err != nil || f == nil {
				t.Fatal("in-bound publication failed", err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := ReadLock(path)
			if err != nil || got != want {
				t.Fatal("in-bound metadata was not returned completely", err)
			}
		})
	}
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized_existing_%v", existing), func(t *testing.T) {
			path := filepath.Join(task43PrivateHome(t), "consoled.lock")
			var before []byte
			var identity task43BoundFileID
			if existing {
				f, err := WriteLock(path, LockInfo{PID: 123, Addr: "127.0.0.1:9477", Home: "original"})
				if err != nil || f == nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				identity = task43BoundIdentity(t, path)
			}
			f, err := WriteLock(path, task43BoundLockInfo(t, windowsMaxLockBytes+1))
			if f != nil {
				if closeErr := f.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}
			if err == nil || f != nil {
				t.Error("oversized publication was accepted", err)
			}
			if existing {
				after, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(after, before) {
					t.Fatal("oversized refusal changed existing metadata", readErr)
				}
				if task43BoundIdentity(t, path) != identity {
					t.Fatal("oversized refusal changed existing identity")
				}
			} else if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatal("oversized refusal created an artifact", statErr)
			}
		})
	}
}
