package adapters

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestCapabilityInstallationIsNotReadiness(t *testing.T) {
	dir := testfixture.PrivateHome(t)
	for _, fixture := range []struct {
		name string
		mode os.FileMode
	}{{"codex", 0700}, {"claude", 0600}, {"opencode", 0700}} {
		body := []byte("#!/bin/sh\nexit 99\n")
		var err error
		if fixture.mode&0111 == 0 {
			err = os.WriteFile(filepath.Join(dir, fixture.name), body, fixture.mode)
		} else {
			err = testfixture.WriteScript(t, filepath.Join(dir, testfixture.ExecutableName(fixture.name)), body, fixture.mode)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	for _, got := range Capabilities(context.Background()) {
		want := "missing"
		if got.ID == "codex" || got.ID == "opencode" {
			want = "present"
		}
		if got.Installation.Status != want {
			t.Fatalf("%+v", got)
		}
		ad, _ := Get(got.ID)
		if got.Isolation != ad.Isolation || got.AccountReadiness.Status != "not_checked" {
			t.Fatalf("%+v", got)
		}
		supported := (got.ID == "codex" || got.ID == "claude") && SelectedPlatform(runtime.GOOS)
		if got.Supported != supported || got.Qualification.Status == "qualified" {
			t.Fatalf("%+v", got)
		}
		for _, banner := range got.CompatibleBanners {
			if !compatibleBanner(got.ID, banner) {
				t.Fatalf("policy disagreement %q", banner)
			}
		}
		if compatibleBanner(got.ID, "99.0.0") {
			t.Fatal("unknown version accepted")
		}
	}
}

func TestCapabilityCancellationDoesNotObserveInstallation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, got := range Capabilities(ctx) {
		if got.Installation.Status != "not_checked" || got.Installation.Code != "check_cancelled" {
			t.Fatalf("%+v", got)
		}
	}
}

func TestSelectedPlatformMatrix(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows", "freebsd", "unknown"} {
		if SelectedPlatform(goos) != (goos == "darwin" || goos == "linux") {
			t.Fatalf("platform %s", goos)
		}
	}
}
