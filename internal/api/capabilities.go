package api

import (
	"runtime"
	"runtime/debug"

	"github.com/batuta-ai/console/internal/domain"
)

type buildCapability struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified *bool  `json:"modified"`
}

type runtimeCapability struct {
	OS        string          `json:"os"`
	Arch      string          `json:"arch"`
	GoVersion string          `json:"go_version"`
	Selected  bool            `json:"selected"`
	Build     buildCapability `json:"build"`
}

type capabilityResponse struct {
	API          string                 `json:"api"`
	Product      string                 `json:"product"`
	Lang         string                 `json:"lang"`
	Integrations []string               `json:"integrations"`
	Adapters     []string               `json:"adapters"`
	Features     map[string]bool        `json:"features"`
	Runtime      runtimeCapability      `json:"runtime"`
	Matrix       releaseMatrix          `json:"matrix"`
	PTY          domain.PTYCapability   `json:"pty"`
	Providers    []domain.CLICapability `json:"providers"`
}

type releaseMatrix struct {
	Platforms     []string              `json:"platforms"`
	Providers     []string              `json:"providers"`
	Qualification domain.CapabilityFact `json:"qualification"`
}

func runningBuild() buildCapability {
	build := buildCapability{Version: "unknown", Revision: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" {
			build.Version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				build.Revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" || setting.Value == "false" {
					modified := setting.Value == "true"
					build.Modified = &modified
				}
			}
		}
	}
	return build
}

func runningRuntime() runtimeCapability {
	return runtimeCapability{OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), Selected: runtime.GOOS == "darwin" || runtime.GOOS == "linux", Build: runningBuild()}
}
