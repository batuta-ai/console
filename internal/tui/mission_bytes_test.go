package tui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyViewReconstructsLargeLosslessSourceAndBoundaries(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.detail.Approval = nil
	f.detail.Content = bytes.Repeat([]byte("multiline\r\ninvalid:\xff\x00\x1b\n"), 3000)
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		_ = json.NewEncoder(w).Encode(f.detail)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	var reconstructed []byte
	reading := false
	ended := false
	for range 10000 {
		view := m.View().Content
		for _, line := range strings.Split(view, "\n") {
			if strings.HasPrefix(line, "Original source bytes;") {
				reading = true
				continue
			}
			if !reading || len(line) < 10 || line[8] != ' ' {
				continue
			}
			offset, err := strconv.ParseUint(line[:8], 16, 32)
			if err != nil {
				continue
			}
			decoded, err := strconv.Unquote(line[9:])
			if err != nil {
				t.Fatal("unsafe/non-lossless source line", line, err)
			}
			if int(offset) != len(reconstructed) {
				t.Fatal("hidden/skipped/repeated source bytes", offset, len(reconstructed))
			}
			reconstructed = append(reconstructed, []byte(decoded)...)
		}
		if strings.Contains(view, "END OF SOURCE") {
			ended = true
			break
		}
		m = journeyKey(t, m, "down")
	}
	if !ended || !bytes.Equal(reconstructed, f.detail.Content) || domain.PlanVersion(reconstructed) != f.detail.Identity.Version {
		t.Fatal("View did not expose every original byte")
	}
	// The wire's maximum is accepted, while a larger reply preserves last good facts.
	f.detail.Content = bytes.Repeat([]byte("x"), domain.MaxPlanBytes)
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	m = journeyKey(t, m, "o")
	last := m.journeyRecord().detail
	if len(last.Content) != domain.MaxPlanBytes || last.Identity.Version != f.detail.Identity.Version {
		t.Fatal("maximum source rejected")
	}
	f.detail.Content = append(f.detail.Content, 'x')
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	m = journeyKey(t, m, "o")
	if m.journeyRecord().detail.Identity != last.Identity || m.journeyRecord().sourceFresh || m.journeyRecord().problem == "" {
		t.Fatal("oversized source replaced confirmed content")
	}
}
func TestJourneyPortugueseFocusedFormsAndReviewHints(t *testing.T) {
	t.Setenv("BATUTA_LANG", "pt")
	m := model{}.selectMission(journeyMission("claude"))
	m = journeyKey(t, m, "b")
	accountText(t, m, "Vincular plano exato existente")
	accountText(t, m, "enter em Revisar")
	m = catalogPaste(m, "q f p x r .batuta/plans/exato.md")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	accountText(t, m, "Confirmar")
	accountText(t, m, "enter confirmar")
	if m.screen != scrRoom || m.journeyRecord().path != "q f p x r .batuta/plans/exato.md" {
		t.Fatal("focused text consumed by global shortcuts")
	}
}
