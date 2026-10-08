package runtime

import (
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// This tests build selection, not native Windows protection or runtime behavior.
func TestWindowsRuntimeExcludesUnixImplementation(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = "windows", "amd64"
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range pkg.GoFiles {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "golang.org/x/sys/unix" {
				t.Errorf("Windows selects Unix implementation %s", name)
			}
		}
	}
}
