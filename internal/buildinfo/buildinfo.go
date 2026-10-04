// Package buildinfo reports embedded binary metadata without inspecting the host.
package buildinfo

import (
	"encoding/json"
	"io"
	"runtime"
	"runtime/debug"
)

const corePath = "github.com/batuta-ai/core"

// ReleaseVersion and ReleaseRevision are optional linker-injected labels, not
// observations or evidence of release qualification.
var ReleaseVersion string
var ReleaseRevision string

type Release struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Authority string `json:"authority"`
}

type Observed struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Modified  *bool  `json:"modified"`
	Authority string `json:"authority"`
}

type Module struct {
	Status             string `json:"status"`
	Version            string `json:"version"`
	ReplacementVersion string `json:"replacement_version"`
	Authority          string `json:"authority"`
}

type Fact struct {
	Status    string `json:"status"`
	Authority string `json:"authority"`
}

type Compatibility struct {
	Console string `json:"console"`
	API     string `json:"api"`
	Core    Fact   `json:"core"`
	Plugin  Fact   `json:"plugin"`
	Stable  Fact   `json:"stable"`
	Native  Fact   `json:"native"`
}

type Info struct {
	GoVersion     string        `json:"go_version"`
	GoAuthority   string        `json:"go_authority"`
	Release       Release       `json:"release"`
	Observed      Observed      `json:"observed"`
	Core          Module        `json:"core"`
	Plugin        Module        `json:"plugin"`
	Compatibility Compatibility `json:"compatibility"`
}

func Current() Info {
	info, _ := debug.ReadBuildInfo()
	return describe(info, runtime.Version(), ReleaseVersion, ReleaseRevision)
}

func describe(info *debug.BuildInfo, goVersion, version, revision string) Info {
	release := Release{Version: "development", Revision: "unknown", Authority: "unset"}
	if version != "" || revision != "" {
		release.Authority = "linker_injected"
		if version != "" {
			release.Version = version
		}
		if revision != "" {
			release.Revision = revision
		}
	}
	result := Info{
		GoVersion: goVersion, GoAuthority: "runtime.Version",
		Release: release, Observed: Observed{Version: "unknown", Revision: "unknown", Authority: "go_build_info"},
		Core:          Module{Status: "missing", Version: "unknown", ReplacementVersion: "none", Authority: "go_build_info"},
		Plugin:        Module{Status: "unknown", Version: "unknown", ReplacementVersion: "unknown", Authority: "no_trusted_manifest"},
		Compatibility: Compatibility{Console: "batuta-console", API: "v0", Core: Fact{Status: "unverified", Authority: "no_qualified_contract"}, Plugin: Fact{Status: "unknown", Authority: "no_trusted_manifest"}, Stable: Fact{Status: "pending", Authority: "release_qualification_required"}, Native: Fact{Status: "pending", Authority: "macos_linux_qualification_required"}},
	}
	if info == nil {
		return result
	}
	if info.Main.Version != "" {
		result.Observed.Version = info.Main.Version
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				result.Observed.Revision = setting.Value
			}
		case "vcs.modified":
			if setting.Value == "true" || setting.Value == "false" {
				modified := setting.Value == "true"
				result.Observed.Modified = &modified
			}
		}
	}
	for _, dep := range info.Deps {
		if dep.Path != corePath {
			continue
		}
		result.Core.Status = "linked"
		result.Core.Version = moduleVersion(dep.Version)
		if dep.Replace != nil {
			result.Core.Status = "replaced"
			result.Core.ReplacementVersion = moduleVersion(dep.Replace.Version)
		}
		break
	}
	return result
}

func moduleVersion(version string) string {
	if version == "" || version == "(devel)" {
		return "development"
	}
	return version
}

// WriteDiagnostic emits one JSON object describing this binary.
func WriteDiagnostic(w io.Writer, binary string) error {
	return json.NewEncoder(w).Encode(struct {
		Binary string `json:"binary"`
		Info
	}{binary, Current()})
}
