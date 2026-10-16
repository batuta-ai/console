//go:build windows

package batuta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unsafe"
)

const preexistingSID = "S-1-5-21-3534651229-1508634349-1091504176-1001"
const preexistingSchema = "batuta-task43-symlinks-v1"

type preexistingCase struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Target    string `json:"target"`
	Directory bool   `json:"directory"`
	SHA256    string `json:"sha256"`
}
type preexistingToken struct {
	SID        string   `json:"sid"`
	Groups     []string `json:"groups"`
	Privileges string   `json:"privileges"`
	Type       uint32   `json:"type"`
	Elevation  uint32   `json:"elevation"`
}
type preexistingManifest struct {
	Schema       string            `json:"schema"`
	SID          string            `json:"sid"`
	RootIdentity string            `json:"root_identity"`
	Token        preexistingToken  `json:"token"`
	Cases        []preexistingCase `json:"cases"`
}

func preexistingBody(id string) []byte {
	switch id {
	case "setup_file", "setup_parent":
		return []byte("synthetic setup profile\r\n")
	case "create_file":
		return []byte("synthetic unchanged private target\x00\n")
	case "create_parent":
		return nil
	case "auth_file":
		return []byte(strings.Repeat("0123456789abcdef", 4))
	default:
		return []byte(`{"pid":123,"addr":"127.0.0.1:12345","home":"synthetic"}`)
	}
}
func preexistingCases() []preexistingCase {
	cases := []preexistingCase{
		{ID: "setup_file", Alias: "cases/setup_file/.batuta/profile.md", Target: "targets/setup_file"},
		{ID: "setup_parent", Alias: "cases/setup_parent/.batuta", Target: "targets/setup_parent", Directory: true},
		{ID: "create_file", Alias: "cases/create_file/existing", Target: "targets/create_file"},
		{ID: "create_parent", Alias: "cases/create_parent/parent", Target: "targets/create_parent", Directory: true},
		{ID: "auth_file", Alias: "cases/auth_file/auth.secret", Target: "targets/auth_file"},
		{ID: "daemon_lock", Alias: "cases/daemon_lock/consoled.lock", Target: "targets/daemon_lock"},
		{ID: "auth_lock", Alias: "cases/auth_lock/auth.secret.lock", Target: "targets/auth_lock"},
		{ID: "read_lock", Alias: "cases/read_lock/consoled.lock", Target: "targets/read_lock"},
	}
	for i := range cases {
		cases[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(preexistingBody(cases[i].ID)))
	}
	return cases
}
func preexistingCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func preexistingTokenState(t *testing.T) preexistingToken {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	u, err := token.GetTokenUser()
	preexistingCheck(t, err)
	state := preexistingToken{SID: u.User.Sid.String()}
	if state.SID != preexistingSID {
		t.Fatal("unexpected ordinary SID", state.SID)
	}
	groups, err := token.GetTokenGroups()
	preexistingCheck(t, err)
	for _, g := range groups.AllGroups() {
		state.Groups = append(state.Groups, fmt.Sprintf("%s:%08x", g.Sid.String(), g.Attributes))
		if g.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && g.Attributes&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("enabled Administrators group")
		}
	}
	sort.Strings(state.Groups)
	var size uint32
	for class, value := range map[uint32]*uint32{windows.TokenType: &state.Type, windows.TokenElevationType: &state.Elevation} {
		preexistingCheck(t, windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(value)), 4, &size))
	}
	if state.Type != 1 || state.Elevation != 3 {
		t.Fatalf("requires limited PRIMARY token: %+v", state)
	}
	err = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		t.Fatal("token sizing", err)
	}
	data := make([]byte, size)
	preexistingCheck(t, windows.GetTokenInformation(token, windows.TokenPrivileges, &data[0], size, &size))
	p := (*windows.Tokenprivileges)(unsafe.Pointer(&data[0]))
	if p.PrivilegeCount != 5 {
		t.Fatalf("ordinary privilege count=%d", p.PrivilegeCount)
	}
	for _, privilege := range p.AllPrivileges() {
		if privilege.Luid.HighPart == 0 && (privilege.Luid.LowPart == 17 || privilege.Luid.LowPart == 18 || privilege.Luid.LowPart == 35) {
			t.Fatal("Backup/Restore/Symlink privilege present")
		}
	}
	state.Privileges = hex.EncodeToString(data)
	var thread windows.Token
	err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &thread)
	if err == nil {
		windows.CloseHandle(windows.Handle(thread))
		t.Fatal("unexpected impersonation token")
	}
	if !errors.Is(err, windows.ERROR_NO_TOKEN) {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	preexistingCheck(t, err)
	t.Logf("ordinary token=%s", encoded)
	return state
}
func preexistingEnabled(t *testing.T, phase string) bool {
	t.Helper()
	got := os.Getenv("BATUTA_TASK43_PHASE")
	if got == "" && !strings.Contains(flag.Lookup("test.run").Value.String(), "Task43Preexisting") {
		return false
	}
	if got != phase {
		t.Fatalf("requires BATUTA_TASK43_PHASE=%s", phase)
	}
	return true
}

// Handles omit share-delete; every component is pinned and checked before descent.
func preexistingPhysical(t *testing.T, path string) windows.Handle {
	t.Helper()
	if !filepath.IsAbs(path) || len(filepath.VolumeName(path)) != 2 || filepath.Clean(path) != path {
		t.Fatal("noncanonical local path", path)
	}
	parts := strings.Split(path[3:], `\`)
	current := path[:3]
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		name, err := windows.UTF16PtrFromString(current)
		preexistingCheck(t, err)
		h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		preexistingCheck(t, err)
		t.Cleanup(func() { preexistingCheck(t, windows.CloseHandle(h)) })
		var info windows.ByHandleFileInformation
		preexistingCheck(t, windows.GetFileInformationByHandle(h, &info))
		if i == len(parts)-1 {
			return h
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			t.Fatal("unsafe ancestor", current)
		}
	}
	t.Fatal("missing physical leaf")
	return 0
}
func preexistingSecurity(t *testing.T, h windows.Handle) string {
	t.Helper()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	preexistingCheck(t, err)
	owner, _, err := sd.Owner()
	preexistingCheck(t, err)
	if owner == nil || owner.String() != preexistingSID {
		t.Fatal("fixture owner differs from ordinary SID")
	}
	control, _, err := sd.Control()
	preexistingCheck(t, err)
	acl, _, err := sd.DACL()
	preexistingCheck(t, err)
	if control&windows.SE_DACL_PROTECTED == 0 || acl == nil || acl.AceCount != 1 {
		t.Fatal("fixture requires protected sole-SID DACL")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	preexistingCheck(t, windows.GetAce(acl, 0, &ace))
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || sid.String() != preexistingSID || uint32(ace.Mask) != 0x1f01ff {
		t.Fatal("fixture DACL is not sole-SID FullControl")
	}
	var info windows.ByHandleFileInformation
	preexistingCheck(t, windows.GetFileInformationByHandle(h, &info))
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && ace.Header.AceFlags&3 != 3 {
		t.Fatal("directory grant must inherit to files and directories")
	}
	return sd.String()
}
func preexistingIdentity(t *testing.T, h windows.Handle) string {
	t.Helper()
	var info windows.ByHandleFileInformation
	preexistingCheck(t, windows.GetFileInformationByHandle(h, &info))
	return fmt.Sprintf("%08x:%08x:%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
}
func preexistingLoad(t *testing.T) (string, preexistingManifest) {
	t.Helper()
	path := os.Getenv("BATUTA_TASK43_MANIFEST")
	if filepath.Base(path) != "manifest.json" {
		t.Fatal("mandatory manifest.json")
	}
	root := filepath.Dir(path)
	suffix := strings.TrimPrefix(filepath.Base(root), "task43-symlinks-")
	raw, err := hex.DecodeString(suffix)
	if err != nil || len(raw) != 16 || !strings.HasPrefix(filepath.Base(root), "task43-symlinks-") || suffix != hex.EncodeToString(raw) {
		t.Fatal("unbound fixture root")
	}
	rh := preexistingPhysical(t, root)
	preexistingSecurity(t, rh)
	preexistingSecurity(t, preexistingPhysical(t, path))
	f, err := consoleruntime.OpenPrivateFile(path)
	preexistingCheck(t, err)
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 32769))
	decoder.DisallowUnknownFields()
	var m preexistingManifest
	preexistingCheck(t, decoder.Decode(&m))
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatal("trailing manifest data", err)
	}
	if m.Schema != preexistingSchema || m.SID != preexistingSID || m.RootIdentity != preexistingIdentity(t, rh) || !reflect.DeepEqual(m.Cases, preexistingCases()) {
		t.Fatal("manifest differs from fixed eight-case contract")
	}
	if !reflect.DeepEqual(m.Token, preexistingTokenState(t)) {
		t.Fatal("ordinary token differs from preparation")
	}
	t.Cleanup(func() {
		if !reflect.DeepEqual(m.Token, preexistingTokenState(t)) {
			t.Error("ordinary token changed")
		}
	})
	return root, m
}
func preexistingPositivePath(t *testing.T, path string, directory bool) {
	t.Helper()
	var f *os.File
	var err error
	if directory {
		f, err = consoleruntime.OpenPrivateDirectory(path)
	} else {
		f, err = consoleruntime.OpenPrivateFile(path)
	}
	preexistingCheck(t, err)
	preexistingCheck(t, f.Close())
	preexistingSecurity(t, preexistingPhysical(t, path))
}
func preexistingAlias(t *testing.T, root string, c preexistingCase) {
	t.Helper()
	alias, target := filepath.Join(root, filepath.FromSlash(c.Alias)), filepath.Join(root, filepath.FromSlash(c.Target))
	preexistingPositivePath(t, filepath.Dir(alias), true)
	preexistingPositivePath(t, target, c.Directory)
	st, err := os.Lstat(alias)
	preexistingCheck(t, err)
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("physical alias is not a symlink")
	}
	dest, err := os.Readlink(alias)
	preexistingCheck(t, err)
	if dest != target {
		t.Fatalf("link destination=%q want=%q", dest, target)
	}
	h := preexistingPhysical(t, alias)
	preexistingSecurity(t, h)
	var info windows.ByHandleFileInformation
	preexistingCheck(t, windows.GetFileInformationByHandle(h, &info))
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != c.Directory || info.NumberOfLinks != 1 {
		t.Fatal("wrong physical alias attributes")
	}
	var buffer [16 * 1024]byte
	var count uint32
	preexistingCheck(t, windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buffer[0], uint32(len(buffer)), &count, nil))
	if count < 8 || binary.LittleEndian.Uint32(buffer[:4]) != windows.IO_REPARSE_TAG_SYMLINK {
		t.Fatal("requires IO_REPARSE_TAG_SYMLINK")
	}
	if c.ID == "create_parent" {
		entries, err := os.ReadDir(alias)
		preexistingCheck(t, err)
		if len(entries) != 0 {
			t.Fatal("create parent target must be empty")
		}
	} else {
		if c.Directory {
			alias = filepath.Join(alias, "profile.md")
			target = filepath.Join(target, "profile.md")
			preexistingPositivePath(t, target, false)
		}
		for _, path := range []string{target, alias} {
			body, err := os.ReadFile(path)
			preexistingCheck(t, err)
			if !bytes.Equal(body, preexistingBody(c.ID)) || fmt.Sprintf("%x", sha256.Sum256(body)) != c.SHA256 {
				t.Fatal("invalid synthetic target/following bytes", path)
			}
		}
	}
	t.Logf("physical alias verified: case=%s tag=SYMLINK directory=%t owner=%s", c.ID, c.Directory, preexistingSID)
}

type preexistingObject struct {
	Identity    string
	Security    string
	Mode        os.FileMode
	Attributes  uint32
	Links       uint32
	Destination string
	Bytes       string
}

func preexistingSnapshot(t *testing.T, root string) map[string]preexistingObject {
	t.Helper()
	result := make(map[string]preexistingObject)
	preexistingCheck(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "controls" {
			return filepath.SkipDir
		}
		h := preexistingPhysical(t, path)
		var info windows.ByHandleFileInformation
		preexistingCheck(t, windows.GetFileInformationByHandle(h, &info))
		st, err := os.Lstat(path)
		preexistingCheck(t, err)
		obj := preexistingObject{Identity: preexistingIdentity(t, h), Security: preexistingSecurity(t, h), Mode: st.Mode(), Attributes: info.FileAttributes, Links: info.NumberOfLinks}
		if entry.Type()&os.ModeSymlink != 0 {
			dest, err := os.Readlink(path)
			preexistingCheck(t, err)
			obj.Destination = dest
		} else if !entry.IsDir() {
			data, err := os.ReadFile(path)
			preexistingCheck(t, err)
			obj.Bytes = string(data)
		}
		result[rel] = obj
		return nil
	}))
	return result
}
func preexistingUnchanged(t *testing.T, root string, before map[string]preexistingObject) {
	t.Helper()
	after := preexistingSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fixture identity/mode/security/bytes/destination/namespace changed")
	}
	encoded, err := json.Marshal(after)
	preexistingCheck(t, err)
	t.Logf("unchanged physical snapshot=%s", encoded)
}

func TestTask43PreexistingSetup(t *testing.T) {
	if !preexistingEnabled(t, "consume") {
		return
	}
	root, m := preexistingLoad(t)
	for _, c := range m.Cases[:2] {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			preexistingAlias(t, root, c)
			before := preexistingSnapshot(t, root)
			defer preexistingUnchanged(t, root, before)
			workspace, err := os.OpenRoot(filepath.Join(root, "cases", c.ID))
			preexistingCheck(t, err)
			defer workspace.Close()
			got, err := readSetupFile(workspace, ".batuta/profile.md")
			if got != nil || err == nil || errors.Is(err, os.ErrPermission) {
				t.Fatal("setup reader accepted alias or failed on permissions", err)
			}
			f, err := openSetupFile(workspace, ".batuta/profile.md")
			if f != nil {
				f.Close()
				t.Fatal("setup opener adopted alias")
			}
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatal("requires typed opener alias rejection", err)
			}
		})
	}
	t.Run("controls", func(t *testing.T) {
		t.Run("exact_setup_read", func(t *testing.T) {
			path := filepath.Join(root, "controls", "setup")
			preexistingPositivePath(t, filepath.Join(path, ".batuta"), true)
			preexistingPositivePath(t, filepath.Join(path, ".batuta", "profile.md"), false)
			workspace, err := os.OpenRoot(path)
			preexistingCheck(t, err)
			defer workspace.Close()
			got, err := readSetupFile(workspace, ".batuta/profile.md")
			preexistingCheck(t, err)
			if !bytes.Equal(got, preexistingBody("setup_file")) {
				t.Fatal("setup control bytes differ")
			}
		})
	})
}
