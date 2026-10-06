package batuta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/core/loop"
)

func TestPublishedApprovedSnapshotValidation(t *testing.T) {
	root := t.TempDir()
	content := []byte("# Contract\n\nStatus: proposed\n\nExact approved bytes.\n")
	path := filepath.Join(root, ".batuta", "plans", "contract.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	snapshot := loop.ApprovedPlanSnapshot{Slug: "contract", Path: ".batuta/plans/contract.md", ReceiptID: "console-receipt-1", ContentDigest: hex.EncodeToString(digest[:]), Content: content}
	for _, tc := range []struct {
		name   string
		mutate func(*loop.ApprovedPlanSnapshot)
	}{
		{"receipt absent", func(s *loop.ApprovedPlanSnapshot) { s.ReceiptID = "" }},
		{"receipt whitespace", func(s *loop.ApprovedPlanSnapshot) { s.ReceiptID = " receipt" }},
		{"receipt encoding", func(s *loop.ApprovedPlanSnapshot) { s.ReceiptID = string([]byte{0xff}) }},
		{"receipt oversize", func(s *loop.ApprovedPlanSnapshot) { s.ReceiptID = strings.Repeat("r", 1025) }},
		{"foreign path", func(s *loop.ApprovedPlanSnapshot) { s.Path = ".batuta/plans/foreign.md" }},
		{"path traversal", func(s *loop.ApprovedPlanSnapshot) { s.Path = ".batuta/plans/../plans/contract.md" }},
		{"slug traversal", func(s *loop.ApprovedPlanSnapshot) { s.Slug = "../contract" }},
		{"digest", func(s *loop.ApprovedPlanSnapshot) { s.ContentDigest = strings.Repeat("0", 64) }},
		{"uppercase digest", func(s *loop.ApprovedPlanSnapshot) { s.ContentDigest = strings.ToUpper(s.ContentDigest) }},
		{"changed bytes", func(s *loop.ApprovedPlanSnapshot) { s.Content = append(append([]byte(nil), s.Content...), '\n') }},
		{"empty content", func(s *loop.ApprovedPlanSnapshot) { s.Content = nil }},
		{"oversize content", func(s *loop.ApprovedPlanSnapshot) { s.Content = bytes.Repeat([]byte("x"), (1<<20)+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := snapshot
			tc.mutate(&invalid)
			runner, err := loop.New(context.Background(), loop.Options{Workspace: root, ApprovedSnapshot: &invalid, DeliveryID: "contract-delivery"})
			if runner != nil || !errors.Is(err, loop.ErrSnapshotInvalid) {
				t.Fatalf("runner=%v error=%v", runner, err)
			}
		})
	}
	// A deliberately invalid delivery stops after native snapshot validation,
	// before workspace preparation, journal ownership or executor submission.
	for _, snapshotPath := range []string{".batuta/plans/contract.md", ".batuta/plan-contract.md"} {
		valid := snapshot
		valid.Path = snapshotPath
		runner, err := loop.New(context.Background(), loop.Options{Workspace: root, ApprovedSnapshot: &valid, DeliveryID: "../invalid"})
		if runner != nil || err == nil || err.Error() != "loop: invalid fresh delivery id" {
			t.Fatalf("valid snapshot path %q: runner=%v error=%v", snapshotPath, runner, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, content) {
		t.Fatal("snapshot validation changed source bytes")
	}
	entries, err := os.ReadDir(filepath.Join(root, ".batuta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "plans" {
		t.Fatalf("validation created workspace state: %v", entries)
	}
}
