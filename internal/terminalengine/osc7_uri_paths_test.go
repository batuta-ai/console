package terminalengine

import (
	"runtime"
	"testing"
)

func TestOSC7URIPathsIndependentOfReceivingOS(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          WorkingDirectory
	}{
		{"raw-unix", "/work/project one", WorkingDirectory{Path: "/work/project one", Local: true}},
		{"local-unix", "file://localhost/work/project%20one", WorkingDirectory{Host: "localhost", Path: "/work/project one", Local: true}},
		{"remote-unix", "file://BUILD-HOST.EXAMPLE/work/project%20one", WorkingDirectory{Host: "build-host.example", Path: "/work/project one"}},
		{"local-drive", "file:///C:/work/project%20one", WorkingDirectory{Path: "/C:/work/project one", Local: true}},
		{"loopback-drive", "file://localhost/D:/work/%E7%8C%AB", WorkingDirectory{Host: "localhost", Path: "/D:/work/猫", Local: true}},
		{"remote-drive", "file://BUILD-HOST.EXAMPLE/C:/work/project%20one", WorkingDirectory{Host: "build-host.example", Path: "/C:/work/project one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := NewParser(NewScreen(3, 40))
			var observed []WorkingDirectory
			parser.SetCWDCallback(func(location WorkingDirectory) { observed = append(observed, location) })
			parser.Feed([]byte("\x1b]7;" + tc.payload + "\x1b\\"))
			if len(observed) != 1 || observed[0] != tc.want {
				t.Fatalf("OSC7 %q callbacks = %+v, want %+v", tc.payload, observed, tc.want)
			}
		})
	}
}

func TestOSC7URIPathsRejectInvalidInputs(t *testing.T) {
	for _, payload := range []string{
		"work/project", "file:work/project", "file://build-host.example",
		"file://user@localhost/work", "file://localhost:22/work",
		"file:///work?query", "file:///work#fragment", "file:///work%XX",
		"file:///work%00bad", "file:///work%1Bbad", "file:///work%07bad",
		"file:///work%0Abad", "file:///work%0Dbad",
		"/work\x00bad", "/work\x1bbad", "/work\x07bad", "/work\nbad", "/work\rbad",
	} {
		if location, ok := parseOSC7WorkingDirectory(payload); ok {
			t.Errorf("invalid OSC7 %q accepted: %+v", payload, location)
		}
	}
}

func TestOSC7NativeRawAbsolutePath(t *testing.T) {
	path := "/work/project one"
	if runtime.GOOS == "windows" {
		path = `C:\work\project one`
	}
	location, ok := parseOSC7WorkingDirectory(path)
	if !ok || location != (WorkingDirectory{Path: path, Local: true}) {
		t.Fatalf("native raw absolute path changed: %+v, %v", location, ok)
	}
}
