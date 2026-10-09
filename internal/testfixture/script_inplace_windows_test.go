package testfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestNativeScriptInPlacePreservesLiveSession(t *testing.T) {
	root := testfixture.PrivateHome(t)
	path := filepath.Join(root, "live CLI ação.exe")
	beforeBody := []byte(`#!/bin/sh
old_session() {
  test -t 0 && test -t 1 && test -t 2 || exit 90
  trap 'printf "OLD-TRAP\n"' EXIT
  printf 'OLD-TTY-READY\n'
  IFS= read -r input
  printf 'OLD-ACK:%s\n' "$input"
  IFS= read -r finish
  printf 'OLD-END:%s\n' "$finish"
  exit 44
}
if [ "$1" = '--version' ]; then printf 'codex-cli 0.160.0\n'; exit 0; fi
old_session
`)
	afterBody := []byte("#!/bin/sh\nif [ \"$1\" = '--version' ]; then printf 'codex-cli 99.0.0\\n'; exit 0; fi\nprintf 'NEW-BODY'; exit 45\n")
	if err := testfixture.WriteScript(t, path, beforeBody, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{root, path, path + ".shell-script", path + ".shell-config.json"}
	before := scriptUpdateInspect(t, paths)
	runner, environment := scriptNativeRunner(t, root)
	pending, err := runner.Manager.StartPending(domain.Session{ID: "inplace-live", Argv: []string{path}, Cols: 80, Rows: 24, Owner: "tui", OwnerClient: "inplace-client"}, environment, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.Manager.SubscribeTerminal("inplace-live")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output strings.Builder
	readUntil := func(want string) {
		t.Helper()
		for !strings.Contains(output.String(), want) {
			event, err := sub.Next(ctx)
			output.Write(event.Data)
			if err != nil {
				t.Fatal("live session acknowledgement missing", want, output.String(), err)
			}
		}
	}
	readUntil("OLD-TTY-READY")
	if pending.Snapshot().Process != "running" || pending.Snapshot().EndedAt != nil {
		t.Fatal("original interactive session is not alive", pending.Snapshot())
	}
	if err := testfixture.UpdateScriptInPlace(t, path, afterBody, 0755); err != nil {
		t.Fatal("update while actual selected PE/session is alive", err)
	}
	after := scriptUpdateInspect(t, paths)
	for _, target := range paths {
		a, b := before[target], after[target]
		if target != path+".shell-script" && target != path+".shell-config.json" && a.Data != b.Data {
			t.Fatal("immutable image bytes changed", target)
		}
		a.Data, b.Data = "", ""
		if a != b {
			t.Fatal("original object identity/type/owner/protected DACL changed", target, a, b)
		}
	}
	if !bytes.Equal([]byte(after[path+".shell-script"].Data), afterBody) {
		t.Fatal("updated body differs")
	}
	if err := runner.Manager.InputContext(ctx, "inplace-live", "inplace-client", []byte("ação-after-update\r")); err != nil {
		t.Fatal(err)
	}
	readUntil("OLD-ACK:ação-after-update")
	for _, check := range []struct {
		args   []string
		stdout string
		exit   int
	}{{[]string{"--version"}, "codex-cli 99.0.0\n", 0}, {nil, "NEW-BODY", 45}} {
		result, err := runner.Run(ctx, publication.Command{Executable: path, Args: check.args, Directory: root})
		if err != nil || string(result.Stdout) != check.stdout || result.ExitCode != check.exit || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
			t.Fatal("new actual invocation did not observe exact updated script", result, err)
		}
	}
	if pending.Snapshot().Process != "running" || pending.Snapshot().EndedAt != nil {
		t.Fatal("original live session lost while new invocation ran", pending.Snapshot())
	}
	if err := runner.Manager.InputContext(ctx, "inplace-live", "inplace-client", []byte("joined\r")); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := sub.Next(ctx)
		output.Write(event.Data)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("original live session did not join", output.String(), err)
		}
	}
	if !strings.Contains(output.String(), "OLD-END:joined") || !strings.Contains(output.String(), "OLD-TRAP") || pending.Snapshot().Process != "exited" || pending.Snapshot().EndedAt == nil {
		t.Fatal("original exit/trap/join missing", output.String(), pending.Snapshot())
	}
	t.Logf("actual live original ConPTY acknowledgement/new version/new body/join: %q", output.String())
}

func TestNativeScriptInPlaceSecondWritableRefusalHasNoEffects(t *testing.T) {
	root := testfixture.PrivateHome(t)
	path := filepath.Join(root, "sharing.exe")
	if err := testfixture.WriteScript(t, path, []byte("#!/bin/sh\nprintf 'original'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := path + ".shell-config.json"
	name, err := windows.UTF16PtrFromString(config)
	if err != nil {
		t.Fatal(err)
	}
	held, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("native share-read-only prerequisite", err)
	}
	defer windows.CloseHandle(held)
	paths := []string{root, path, path + ".shell-script", config}
	before := scriptUpdateInspect(t, paths)
	err = testfixture.UpdateScriptInPlace(t, path, []byte("#!/bin/sh\nprintf 'changed'\n"), 0700)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatal("second writable acquisition must physically fail sharing", err)
	}
	if after := scriptUpdateInspect(t, paths); !reflect.DeepEqual(before, after) {
		t.Fatal("second writable acquisition refusal modified existing triple")
	}
}

func TestNativeScriptInPlaceRefusesUnsafeTriples(t *testing.T) {
	for _, operation := range []string{"inplace"} {
		t.Run(operation, func(t *testing.T) {
			for _, kind := range []string{"image", "script", "missing", "invalid", "unknown", "trailing", "executable", "script-path", "shell-path", "image_sha256", "shell_sha256", "script_sha256", "default-owner", "broad-dacl", "symlink", "hardlink", "directory", "foreign-parent"} {
				t.Run(kind, func(t *testing.T) {
					root := testfixture.PrivateHome(t)
					path := filepath.Join(root, "refused.exe")
					if err := testfixture.WriteScript(t, path, []byte("#!/bin/sh\nprintf '%s' 'original'\n"), 0700); err != nil {
						t.Fatal(err)
					}
					script, config := path+".shell-script", path+".shell-config.json"
					paths := []string{root, path, script, config}
					data, err := os.ReadFile(config)
					if err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "image":
						err = os.WriteFile(path, []byte("altered-image"), 0600)
					case "script":
						err = os.WriteFile(script, []byte("altered-script"), 0600)
					case "missing":
						err = os.Remove(config)
					case "invalid":
						err = os.WriteFile(config, []byte("{"), 0600)
					case "trailing":
						err = os.WriteFile(config, append(data, []byte(" {}")...), 0600)
					case "default-owner", "symlink", "hardlink", "directory":
						original, readErr := os.ReadFile(script)
						if readErr != nil {
							t.Fatal(readErr)
						}
						if err := os.Remove(script); err != nil {
							t.Fatal(err)
						}
						if kind == "default-owner" {
							err = os.WriteFile(script, original, 0600)
						} else if kind == "directory" {
							err = os.Mkdir(script, 0700)
						} else {
							target := filepath.Join(root, "actual-target")
							f, err := consolert.CreatePrivateFile(target)
							if err != nil {
								t.Fatal(err)
							}
							_, writeErr := f.Write(original)
							if err := errors.Join(writeErr, f.Close()); err != nil {
								t.Fatal(err)
							}
							paths = append(paths, target)
							if kind == "symlink" {
								err = os.Symlink(target, script)
							} else {
								err = os.Link(target, script)
							}
						}
					case "broad-dacl", "foreign-parent":
						user, err := windows.GetCurrentProcessToken().GetTokenUser()
						if err != nil {
							t.Fatal(err)
						}
						sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;WD)")
						if err != nil {
							t.Fatal(err)
						}
						acl, _, err := sd.DACL()
						if err != nil {
							t.Fatal(err)
						}
						target := script
						if kind == "foreign-parent" {
							target = root
						}
						err = windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
					default:
						var descriptor map[string]string
						if err := json.Unmarshal(data, &descriptor); err != nil {
							t.Fatal(err)
						}
						switch kind {
						case "unknown":
							descriptor["unknown"] = "unexpected"
						case "executable":
							descriptor["executable"] = filepath.Join(root, "different.exe")
						case "script-path":
							descriptor["script"] = filepath.Join(root, "different-script")
						case "shell-path":
							descriptor["shell"] = path
						default:
							descriptor[kind] = strings.Repeat("0", 64)
						}
						data, err = json.Marshal(descriptor)
						if err == nil {
							err = os.WriteFile(config, data, 0600)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					before := scriptUpdateInspect(t, paths)
					if kind == "default-owner" {
						sd, err := windows.GetNamedSecurityInfo(script, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
						if err != nil {
							t.Fatal(err)
						}
						owner, _, err := sd.Owner()
						if err != nil || owner == nil || owner.String() != "S-1-5-32-544" {
							t.Fatal("QA default owner precondition is not actual BA", err)
						}
					}
					if kind == "hardlink" && before[script].Links != 2 || kind == "symlink" && before[script].Mode&os.ModeSymlink == 0 {
						t.Fatal("actual native alias constructor invalid", kind)
					}
					err = testfixture.UpdateScriptInPlace(t, path, []byte("#!/bin/sh\nprintf 'modified'\n"), 0700)
					if err == nil {
						t.Fatal("unsafe existing triple accepted", kind)
					}
					if after := scriptUpdateInspect(t, paths); !reflect.DeepEqual(before, after) {
						t.Fatal("refusal changed native bytes/descriptor/object identity/links/alias", kind)
					}
				})
			}
		})
	}
}

func TestNativeScriptInPlaceRefusesNoncanonicalDescriptor(t *testing.T) {
	for _, operation := range []string{"inplace"} {
		t.Run(operation, func(t *testing.T) {
			for _, kind := range []string{"case-key", "duplicate-key", "whitespace"} {
				t.Run(kind, func(t *testing.T) {
					root := testfixture.PrivateHome(t)
					path := filepath.Join(root, "noncanonical.exe")
					if err := testfixture.WriteScript(t, path, []byte("#!/bin/sh\nprintf 'original'\n"), 0700); err != nil {
						t.Fatal(err)
					}
					config := path + ".shell-config.json"
					data, err := os.ReadFile(config)
					if err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "case-key":
						data = bytes.Replace(data, []byte(`"executable":`), []byte(`"Executable":`), 1)
					case "duplicate-key":
						data = append([]byte(`{"executable":"different-earlier-value",`), data[1:]...)
					default:
						data = append([]byte(" \n"), data...)
					}
					if err := os.WriteFile(config, data, 0600); err != nil {
						t.Fatal(err)
					}
					paths := []string{root, path, path + ".shell-script", config}
					before := scriptUpdateInspect(t, paths)
					err = testfixture.UpdateScriptInPlace(t, path, []byte("#!/bin/sh\nprintf 'changed'\n"), 0700)
					if err == nil {
						t.Fatal("noncanonical generated descriptor accepted", kind)
					}
					if after := scriptUpdateInspect(t, paths); !reflect.DeepEqual(before, after) {
						t.Fatal("noncanonical refusal changed original objects", kind)
					}
				})
			}
		})
	}
}

func TestNativeScriptInPlacePreservesEveryUnsafeObject(t *testing.T) {
	for _, operation := range []string{"inplace"} {
		t.Run(operation, func(t *testing.T) {
			for _, suffix := range []string{"", ".shell-script", ".shell-config.json"} {
				t.Run("target"+suffix, func(t *testing.T) {
					for _, kind := range []string{"default-owner", "broad-dacl", "symlink", "hardlink", "directory"} {
						t.Run(kind, func(t *testing.T) {
							root := testfixture.PrivateHome(t)
							path := filepath.Join(root, "actual-unsafe.exe")
							if err := testfixture.WriteScript(t, path, []byte("#!/bin/sh\nprintf 'original'\n"), 0700); err != nil {
								t.Fatal(err)
							}
							target := path + suffix
							paths := []string{root, path, path + ".shell-script", path + ".shell-config.json"}
							if kind == "broad-dacl" {
								user, err := windows.GetCurrentProcessToken().GetTokenUser()
								if err != nil {
									t.Fatal(err)
								}
								sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;WD)")
								if err != nil {
									t.Fatal(err)
								}
								acl, _, err := sd.DACL()
								if err != nil {
									t.Fatal(err)
								}
								if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
									t.Fatal(err)
								}
								actual, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
								if err != nil {
									t.Fatal(err)
								}
								actualACL, _, err := actual.DACL()
								if err != nil || actualACL == nil || actualACL.AceCount != 2 {
									t.Fatal("actual native broad-DACL constructor invalid", err)
								}
							} else {
								data, err := os.ReadFile(target)
								if err != nil {
									t.Fatal(err)
								}
								if err := os.Remove(target); err != nil {
									t.Fatal(err)
								}
								switch kind {
								case "default-owner":
									err = os.WriteFile(target, data, 0600)
								case "directory":
									err = os.Mkdir(target, 0700)
								default:
									original := filepath.Join(testfixture.PrivateHome(t), "original-object")
									file, err := consolert.CreatePrivateFile(original)
									if err != nil {
										t.Fatal(err)
									}
									_, writeErr := file.Write(data)
									if err := errors.Join(writeErr, file.Close()); err != nil {
										t.Fatal(err)
									}
									paths = append(paths, original)
									if kind == "symlink" {
										err = os.Symlink(original, target)
									} else {
										err = os.Link(original, target)
									}
								}
								if err != nil {
									t.Fatal(err)
								}
							}
							before := scriptUpdateInspect(t, paths)
							if kind == "default-owner" {
								sd, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
								if err != nil {
									t.Fatal(err)
								}
								owner, _, err := sd.Owner()
								if err != nil || owner == nil || owner.String() != "S-1-5-32-544" {
									t.Fatal("actual BA owner prerequisite not met", err)
								}
							}
							if kind == "hardlink" && before[target].Links != 2 || kind == "symlink" && before[target].Mode&os.ModeSymlink == 0 {
								t.Fatal("actual alias/link prerequisite not met", kind)
							}
							var err error
							err = testfixture.UpdateScriptInPlace(t, path, []byte("#!/bin/sh\nprintf 'changed'\n"), 0700)
							if err == nil {
								t.Fatal("unsafe triple member accepted", suffix, kind)
							}
							if after := scriptUpdateInspect(t, paths); !reflect.DeepEqual(before, after) {
								t.Fatal("refusal changed original member/target identity links bytes security", suffix, kind)
							}
						})
					}
				})
			}
		})
	}
}
