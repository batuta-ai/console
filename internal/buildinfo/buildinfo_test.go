package buildinfo

import (
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestObservedAndInjectedRemainSeparate(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Deps: []*debug.Module{{Path: corePath, Version: "v1.1.0-beta.46"}}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "observed"}}}
	got := describe(info, "go-test", "synthetic", "injected")
	if got.Release.Version != "synthetic" || got.Release.Revision != "injected" || got.Observed.Version != "(devel)" || got.Observed.Revision != "observed" || got.Core.Version != "v1.1.0-beta.46" || got.Core.Status != "linked" {
		t.Fatalf("%+v", got)
	}
	if got.Compatibility.Stable.Status != "pending" || got.Plugin.Status != "unknown" {
		t.Fatalf("%+v", got)
	}
}

func TestMissingAndReplacementAreExplicit(t *testing.T) {
	got := describe(nil, "go-test", "", "")
	if got.Release.Version != "development" || got.Release.Revision != "unknown" || got.Observed.Version != "unknown" || got.Core.Status != "missing" {
		t.Fatalf("%+v", got)
	}
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: corePath, Version: "v1.1.0-beta.46", Replace: &debug.Module{Path: "/private/secret", Version: ""}}}}
	got = describe(info, "go-test", "", "")
	data, _ := json.Marshal(got)
	if got.Core.Status != "replaced" || got.Core.ReplacementVersion != "development" || strings.Contains(string(data), "/private/secret") {
		t.Fatalf("%s", data)
	}
}

func TestCurrentActualToolchain(t *testing.T) {
	if got := Current(); got.GoVersion != runtime.Version() {
		t.Fatalf("%+v", got)
	}
}
