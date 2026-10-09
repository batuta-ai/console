package testfixture_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestNativeOwnedScriptUpdateAndRemoval(t *testing.T) {
	root := testfixture.PrivateHome(t)
	path := filepath.Join(root, "original CLI ação.exe")
	bodyA := []byte("#!/bin/sh\nprintf '%s' 'originalA'; exit 44\n")
	bodyB := []byte("#!/bin/sh\nprintf '%s' 'originalB'; exit 45\n")
	if err := testfixture.WriteScript(t, path, bodyA, 0700); err != nil {
		t.Fatal(err)
	}
	runner, _ := scriptNativeRunner(t, root)
	run := func(want string, exit int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := runner.Run(ctx, publication.Command{Executable: path, Directory: root})
		if err != nil || result.ExitCode != exit || string(result.Stdout) != want || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
			t.Fatalf("original script actual native execution: result=%+v err=%v", result, err)
		}
	}
	run("originalA", 44)
	if err := testfixture.RewriteScript(t, path, bodyB, 0755); err != nil {
		t.Fatal(err)
	}
	run("originalB", 45)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, path + ".shell-script", path + ".shell-config.json"} {
		file, err := consolert.OpenPrivateFile(target)
		if err != nil {
			t.Fatal(err)
		}
		var info windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
		sd, sdErr := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err := errors.Join(infoErr, sdErr, file.Close()); err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) || info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
			t.Fatal("rewritten triple ownership/type/link changed", target, info, err)
		}
		control, _, err := sd.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("rewritten triple DACL is not protected", target, err)
		}
	}
	actual, err := os.ReadFile(path + ".shell-script")
	if err != nil || !bytes.Equal(actual, bodyB) {
		t.Fatal("rewritten original script bytes changed", err)
	}
	data, err := os.ReadFile(path + ".shell-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]string
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor["executable"] != path || descriptor["script"] != path+".shell-script" {
		t.Fatal("rewritten triple path binding changed")
	}
	for _, pair := range [][2]string{{"image_sha256", path}, {"script_sha256", path + ".shell-script"}, {"shell_sha256", descriptor["shell"]}} {
		data, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if descriptor[pair[0]] != hex.EncodeToString(sum[:]) {
			t.Fatal("rewritten triple actual digest differs", pair)
		}
	}
	if err := testfixture.RemoveScript(t, path); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, path + ".shell-script", path + ".shell-config.json"} {
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("owned removal left an adjacent file", target, err)
		}
	}
	if err := testfixture.WriteScript(t, path, bodyA, 0700); err != nil {
		t.Fatal("removed triple cannot be exclusively recreated", err)
	}
	run("originalA", 44)
}

type scriptUpdateSnapshot struct {
	Mode                     os.FileMode
	Data, Link, Security     string
	Volume, High, Low, Links uint32
}

func scriptUpdateInspect(t *testing.T, paths []string) map[string]scriptUpdateSnapshot {
	t.Helper()
	result := map[string]scriptUpdateSnapshot{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		item := scriptUpdateSnapshot{Mode: info.Mode()}
		if info.Mode()&os.ModeSymlink != 0 {
			item.Link, err = os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			item.Data = string(data)
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var actual windows.ByHandleFileInformation
			infoErr := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &actual)
			if err := errors.Join(infoErr, file.Close()); err != nil {
				t.Fatal(err)
			}
			item.Volume, item.High, item.Low, item.Links = actual.VolumeSerialNumber, actual.FileIndexHigh, actual.FileIndexLow, actual.NumberOfLinks
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		item.Security = sd.String()
		result[path] = item
	}
	return result
}

func TestNativeOwnedScriptUpdateRefusesUnsafeTriples(t *testing.T) {
	for _, operation := range []string{"rewrite", "remove"} {
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
					if operation == "rewrite" {
						err = testfixture.RewriteScript(t, path, []byte("#!/bin/sh\nprintf 'modified'\n"), 0700)
					} else {
						err = testfixture.RemoveScript(t, path)
					}
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

func TestNativeOwnedScriptUpdateRefusesNoncanonicalDescriptor(t *testing.T) {
	for _, operation := range []string{"rewrite", "remove"} {
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
					if operation == "rewrite" {
						err = testfixture.RewriteScript(t, path, []byte("#!/bin/sh\nprintf 'changed'\n"), 0700)
					} else {
						err = testfixture.RemoveScript(t, path)
					}
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

func TestNativeOwnedScriptUpdatePreservesEveryUnsafeObject(t *testing.T) {
	for _, operation := range []string{"rewrite", "remove"} {
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
							if operation == "rewrite" {
								err = testfixture.RewriteScript(t, path, []byte("#!/bin/sh\nprintf 'changed'\n"), 0700)
							} else {
								err = testfixture.RemoveScript(t, path)
							}
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
