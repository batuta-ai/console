package adapters

import (
	"context"
	"runtime"

	"github.com/batuta-ai/console/internal/domain"
)

func SelectedPlatform(goos string) bool {
	return goos == "darwin" || goos == "linux" || goos == "windows"
}

func compatibleBanners(id string) []string {
	switch id {
	case "codex":
		return []string{"codex-cli 0.160.0"}
	case "claude":
		return []string{"2.1.286 (Claude Code)", "2.1.289 (Claude Code)"}
	default:
		return []string{}
	}
}

func compatibleBanner(id, banner string) bool {
	for _, accepted := range compatibleBanners(id) {
		if banner == accepted {
			return true
		}
	}
	return false
}

// Capabilities only observes PATH. Version and authentication checks require an
// explicit isolated account and remain the ReadinessChecker's responsibility.
func Capabilities(ctx context.Context) []domain.CLICapability {
	result := make([]domain.CLICapability, 0, len(All()))
	for _, ad := range All() {
		selected := ad.ID == "codex" || ad.ID == "claude"
		supported := selected && SelectedPlatform(runtime.GOOS)
		capability := domain.CLICapability{
			ID: ad.ID, CLI: ad.CLI, Selected: selected, Supported: supported, Isolation: ad.Isolation,
			Installation:      domain.CapabilityFact{Status: "not_checked", Code: "check_cancelled"},
			Compatibility:     domain.CapabilityFact{Status: "not_checked", Code: "isolated_account_required"},
			CompatibleBanners: compatibleBanners(ad.ID),
			AccountReadiness:  domain.CapabilityFact{Status: "not_checked", Code: "account_readiness_required"},
			Qualification:     domain.CapabilityFact{Status: "pending", Code: "native_operation_qualification_pending"},
		}
		if !supported {
			code := "platform_unsupported"
			if !selected {
				code = "adapter_unsupported"
			}
			capability.Compatibility = domain.CapabilityFact{Status: "unsupported", Code: code}
			capability.Qualification = domain.CapabilityFact{Status: "unsupported", Code: code}
		}
		if ctx.Err() == nil {
			if _, err := ad.Lookup(); err != nil {
				capability.Installation = domain.CapabilityFact{Status: "missing", Code: "cli_missing"}
			} else {
				capability.Installation = domain.CapabilityFact{Status: "present", Code: "cli_present"}
			}
		}
		result = append(result, capability)
	}
	return result
}
