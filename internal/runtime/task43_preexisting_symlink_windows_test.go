//go:build windows

package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
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
	f, err := OpenPrivateFile(path)
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
		f, err = OpenPrivateDirectory(path)
	} else {
		f, err = OpenPrivateFile(path)
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

func TestTask43PreexistingPrepare(t *testing.T) {
	if !preexistingEnabled(t, "prepare") {
		return
	}
	token := preexistingTokenState(t)
	parent := os.Getenv("BATUTA_TASK43_PARENT")
	// Parent is supplied by the conductor and must already satisfy the private API.
	root, err := NewPrivateTemp(parent, "task43-symlinks-")
	preexistingCheck(t, err)
	t.Log("RETAINED_FIXTURE=" + root)
	mkdir := func(rel string) {
		preexistingCheck(t, EnsurePrivateDirectory(filepath.Join(root, filepath.FromSlash(rel))))
	}
	write := func(rel string, body []byte) {
		f, err := CreatePrivateFile(filepath.Join(root, filepath.FromSlash(rel)))
		preexistingCheck(t, err)
		_, err = f.Write(body)
		closeErr := f.Close()
		preexistingCheck(t, errors.Join(err, closeErr))
	}
	for _, dir := range []string{"cases", "targets", "controls", "controls/setup/.batuta", "controls/create", "controls/auth", "controls/lock"} {
		mkdir(dir)
	}
	cases := preexistingCases()
	for _, c := range cases {
		mkdir(filepath.ToSlash(filepath.Dir(c.Alias)))
		if c.Directory {
			mkdir(c.Target)
			if c.ID == "setup_parent" {
				write(c.Target+"/profile.md", preexistingBody(c.ID))
			}
		} else {
			write(c.Target, preexistingBody(c.ID))
		}
	}
	// Auth takes its independent coordination lock before checking the auth alias.
	write("cases/auth_file/auth.secret.lock", preexistingBody("auth_lock"))
	write("controls/setup/.batuta/profile.md", preexistingBody("setup_file"))
	m := preexistingManifest{Schema: preexistingSchema, SID: preexistingSID, RootIdentity: preexistingIdentity(t, preexistingPhysical(t, root)), Token: token, Cases: cases}
	data, err := json.MarshalIndent(m, "", "  ")
	preexistingCheck(t, err)
	write("manifest.json", data)
	for _, c := range cases {
		preexistingPositivePath(t, filepath.Join(root, filepath.FromSlash(c.Target)), c.Directory)
	}
	if !reflect.DeepEqual(token, preexistingTokenState(t)) {
		t.Fatal("preparation changed token")
	}
	t.Log("TASK43_MANIFEST=" + filepath.Join(root, "manifest.json"))
}

func TestTask43PreexistingRuntime(t *testing.T) {
	if !preexistingEnabled(t, "consume") {
		return
	}
	root, m := preexistingLoad(t)
	for _, c := range m.Cases {
		c := c
		if strings.HasPrefix(c.ID, "setup_") {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			preexistingAlias(t, root, c)
			before := preexistingSnapshot(t, root)
			defer preexistingUnchanged(t, root, before)
			path := filepath.Join(root, filepath.FromSlash(c.Alias))
			var err error
			switch c.ID {
			case "create_file", "create_parent":
				subject := path
				if c.Directory {
					subject = filepath.Join(path, "must-not-exist")
				}
				f, createErr := CreatePrivateFile(subject)
				if f != nil {
					f.Close()
					t.Fatal("CreatePrivateFile returned a handle")
				}
				err = createErr
				if c.ID == "create_file" {
					if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrInvalid) {
						t.Fatal("unexpected exclusive-create status", err)
					}
					t.Logf("actual FILE_CREATE error: %T %v; ErrExist=%t ErrInvalid=%t", err, err, errors.Is(err, os.ErrExist), errors.Is(err, os.ErrInvalid))
					var errno syscall.Errno
					if errors.As(err, &errno) {
						t.Logf("actual FILE_CREATE Win32 errno=%d", uint32(errno))
					}
					f, err = OpenPrivateFile(path)
					if f != nil {
						f.Close()
						t.Fatal("OpenPrivateFile adopted same alias")
					}
				}
			case "auth_file", "auth_lock":
				var token string
				token, err = LoadOrCreateAuth(filepath.Join(filepath.Dir(path), "auth.secret"))
				if token != "" {
					t.Fatal("alias returned/published token")
				}
			case "daemon_lock":
				f, lockErr := WriteLock(path, LockInfo{PID: 123, Addr: "127.0.0.1:12345", Home: "synthetic"})
				if f != nil {
					f.Close()
					t.Fatal("WriteLock adopted alias")
				}
				err = lockErr
			case "read_lock":
				var info LockInfo
				info, err = ReadLock(path)
				if info != (LockInfo{}) {
					t.Fatal("ReadLock returned metadata", info)
				}
			default:
				t.Fatal("unknown case")
			}
			if !errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
				t.Fatal("requires typed alias rejection", err)
			}
			if c.ID == "daemon_lock" || c.ID == "auth_lock" {
				_, err := os.Lstat(filepath.Join(filepath.Dir(path), "auth.secret"))
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unexpected auth publication", err)
				}
			}
		})
	}
	t.Run("controls", func(t *testing.T) {
		t.Run("fresh_private_create", func(t *testing.T) {
			path := filepath.Join(root, "controls", "create", "fresh")
			f, err := CreatePrivateFile(path)
			preexistingCheck(t, err)
			_, err = f.Write(preexistingBody("create_file"))
			preexistingCheck(t, errors.Join(err, f.Close()))
			preexistingPositivePath(t, path, false)
			got, err := os.ReadFile(path)
			preexistingCheck(t, err)
			if !bytes.Equal(got, preexistingBody("create_file")) {
				t.Fatal("private create bytes")
			}
		})
		t.Run("repeated_auth", func(t *testing.T) {
			path := filepath.Join(root, "controls", "auth", "auth.secret")
			first, err := LoadOrCreateAuth(path)
			preexistingCheck(t, err)
			second, err := LoadOrCreateAuth(path)
			preexistingCheck(t, err)
			if len(first) != 64 || first != second {
				t.Fatal("auth control changed token")
			}
			preexistingPositivePath(t, path, false)
		})
		t.Run("lock_roundtrip", func(t *testing.T) {
			path := filepath.Join(root, "controls", "lock", "consoled.lock")
			want := LockInfo{PID: 123, Addr: "127.0.0.1:12345", Home: "synthetic"}
			f, err := WriteLock(path, want)
			preexistingCheck(t, err)
			got, err := ReadLock(path)
			closeErr := f.Close()
			preexistingCheck(t, errors.Join(err, closeErr))
			if got != want {
				t.Fatal("lock roundtrip differs")
			}
			preexistingPositivePath(t, path, false)
		})
	})
}

func TestTask43PreexistingCleanup(t *testing.T) {
	if !preexistingEnabled(t, "cleanup") {
		return
	}
	var root string
	var before map[string]preexistingObject
	if !t.Run("validate_owned_fixture", func(t *testing.T) {
		var m preexistingManifest
		root, m = preexistingLoad(t)
		for _, c := range m.Cases {
			preexistingAlias(t, root, c)
		}
		before = preexistingSnapshot(t, root)
		// Controls are the only namespace excluded from consumer nonmutation checks.
		controls := preexistingSnapshot(t, filepath.Join(root, "controls"))
		for name, object := range controls {
			before[filepath.Join("controls", name)] = object
		}
		allowed := map[string]bool{".": true, "manifest.json": true, "cases": true, "targets": true, "controls": true}
		add := func(path string) {
			for path != "." {
				allowed[path] = true
				path = filepath.Dir(path)
			}
		}
		for _, c := range m.Cases {
			add(filepath.FromSlash(c.Alias))
			add(filepath.FromSlash(c.Target))
		}
		for _, name := range []string{"targets/setup_parent/profile.md", "cases/auth_file/auth.secret.lock", "controls/setup/.batuta/profile.md", "controls/create/fresh", "controls/auth/auth.secret", "controls/auth/auth.secret.lock", "controls/lock/consoled.lock"} {
			add(filepath.FromSlash(name))
		}
		for name := range before {
			if !allowed[name] {
				t.Fatal("unrecognized entry; retain fixture for reconciliation", name)
			}
		}
	}) {
		return
	}
	// Validation handles have closed. Bind each removal to the recorded object ID,
	// with handle-relative leaf opens that never follow reparse points.
	t.Logf("cleanup current owned root=%s identity=%s", root, before["."].Identity)
	parent, err := OpenPrivateDirectory(filepath.Dir(root))
	preexistingCheck(t, err)
	defer parent.Close()
	var remove func(windows.Handle, string, string)
	remove = func(parent windows.Handle, leaf, relative string) {
		name, err := windows.NewNTUnicodeString(leaf)
		preexistingCheck(t, err)
		oa := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
		oa.Length = uint32(unsafe.Sizeof(oa))
		var h windows.Handle
		var status windows.IO_STATUS_BLOCK
		preexistingCheck(t, windows.NtCreateFile(&h, windows.DELETE|windows.READ_CONTROL|windows.FILE_GENERIC_READ, &oa, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0))
		defer windows.CloseHandle(h)
		want := before[relative]
		if preexistingIdentity(t, h) != want.Identity || preexistingSecurity(t, h) != want.Security {
			t.Fatal("cleanup object rebound; retain fixture", relative)
		}
		if want.Mode.IsDir() && want.Mode&os.ModeSymlink == 0 {
			var children []string
			for path := range before {
				if path != relative && filepath.Dir(path) == relative {
					children = append(children, path)
				}
			}
			sort.Strings(children)
			// Preserve the manifest for reconciliation until all data directories are gone.
			if relative == "." {
				for i, child := range children {
					if child == "manifest.json" {
						children = append(append(children[:i], children[i+1:]...), child)
						break
					}
				}
			}
			for _, child := range children {
				remove(h, filepath.Base(child), child)
			}
		}
		delete := byte(1)
		preexistingCheck(t, windows.NtSetInformationFile(h, &windows.IO_STATUS_BLOCK{}, &delete, 1, windows.FileDispositionInformation))
	}
	remove(windows.Handle(parent.Fd()), filepath.Base(root), ".")
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture remains; reconcile this root only", root, err)
	}
}
