package testfixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

// UpdateScriptInPlace is for controlled fixture updates with no simultaneous
// launch or mutation. An old session must already have read its command body.
// Body/descriptor writes are not atomic; later I/O failure leaves a failed fixture.
func UpdateScriptInPlace(t testing.TB, path string, body []byte, _ os.FileMode) (err error) {
	t.Helper()
	if len(body) > 64<<20 {
		return os.ErrInvalid
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return os.ErrInvalid
	}
	parent, err := consolert.OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	paths := []string{path, path + ".shell-script", path + ".shell-config.json"}
	files := []*os.File{}
	closeFiles := func() error {
		var err error
		for _, file := range files {
			err = errors.Join(err, file.Close())
		}
		files = nil
		return err
	}
	defer func() { err = errors.Join(err, closeFiles()) }()
	contents := make([][]byte, len(paths))
	objects := make([]inplaceScriptObject, len(paths))
	for i, target := range paths {
		file, err := consolert.OpenPrivateFile(target)
		if err != nil {
			return err
		}
		files = append(files, file)
		objects[i], err = inspectInplaceScript(file)
		if err != nil {
			return err
		}
		limit := int64(64 << 20)
		if i == 2 {
			limit = 64 << 10
		}
		contents[i], err = io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil {
			return err
		}
		if int64(len(contents[i])) > limit {
			return os.ErrInvalid
		}
	}
	var descriptor nativeScriptDescriptor
	decoder := json.NewDecoder(bytes.NewReader(contents[2]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return os.ErrInvalid
	}
	if nativeFixtureGitErr != nil {
		return nativeFixtureGitErr
	}
	shell := filepath.Join(filepath.Dir(filepath.Dir(nativeFixtureGit)), "usr", "bin", "sh.exe")
	shellBytes, err := os.ReadFile(shell)
	if err != nil {
		return err
	}
	image, err := nativeScriptExecutable(t)
	if err != nil {
		return err
	}
	want := nativeScriptDescriptor{Executable: path, ImageSHA: fixtureDigest(image), Shell: shell, ShellSHA: fixtureDigest(shellBytes), Script: paths[1], ScriptSHA: fixtureDigest(contents[1])}
	canonical, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if descriptor != want || !bytes.Equal(contents[0], image) || !bytes.Equal(contents[2], canonical) {
		return os.ErrPermission
	}

	want.ScriptSHA = fixtureDigest(body)
	updated, err := json.Marshal(want)
	if err != nil {
		return err
	}
	writable := []*os.File{}
	defer func() {
		for _, file := range writable {
			err = errors.Join(err, file.Close())
		}
	}()
	for i := 1; i < len(paths); i++ {
		file, openErr := openInplaceScript(parent, paths[i])
		if openErr != nil {
			return openErr
		}
		writable = append(writable, file)
		actual, checkErr := inspectInplaceScript(file)
		if checkErr != nil {
			return checkErr
		}
		if actual != objects[i] {
			return os.ErrPermission
		}
	}
	// Both existing writable objects are acquired and checked before either changes.
	for i, data := range [][]byte{body, updated} {
		file := writable[i]
		if err := file.Truncate(0); err != nil {
			return err
		}
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		if err := file.Sync(); err != nil {
			return err
		}
	}
	for i, file := range files {
		actual, checkErr := inspectInplaceScript(file)
		if checkErr != nil {
			return checkErr
		}
		if actual != objects[i] {
			return os.ErrPermission
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		expected := image
		if i == 1 {
			expected = body
		} else if i == 2 {
			expected = updated
		}
		actualBytes, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
		if err != nil {
			return err
		}
		if !bytes.Equal(actualBytes, expected) {
			return os.ErrInvalid
		}
	}
	return nil
}

type inplaceScriptObject struct {
	volume, high, low, links, attributes uint32
	security                             string
}

func inspectInplaceScript(file *os.File) (inplaceScriptObject, error) {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return inplaceScriptObject{}, fmt.Errorf("in-place file identity: %w", err)
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return inplaceScriptObject{}, os.ErrInvalid
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return inplaceScriptObject{}, fmt.Errorf("in-place owner/DACL: %w", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return inplaceScriptObject{}, err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return inplaceScriptObject{}, os.ErrPermission
	}
	return inplaceScriptObject{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.NumberOfLinks, info.FileAttributes, sd.String()}, nil
}

func openInplaceScript(parent *os.File, path string) (*os.File, error) {
	name := filepath.Base(path)
	if !filepath.IsLocal(name) || strings.ContainsAny(name, "/\\<>:\"|?*\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return nil, os.ErrInvalid
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	err = windows.NtCreateFile(&handle, windows.FILE_WRITE_DATA|windows.READ_CONTROL|windows.SYNCHRONIZE, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		var native windows.NTStatus
		if errors.As(err, &native) {
			err = native.Errno()
		}
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
