package batuta

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestApprovedExecutionEvidenceKeepsTerminalAndProjectionOnOneCapture(t *testing.T) {
	root, binding, approval := supervisedProjectionFixture(t)
	ctx := context.Background()
	plan, before, err := approvedDeliverySnapshot(ctx, root, binding, approval)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.Append(binding.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"canceled"}`), Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	old, err := approvedExecutionEvidence(ctx, root, binding, plan, before)
	if err != nil || old.OpenedDigest != binding.OpenedDigest || old.TerminalDigest != "" || old.TerminalState != "" || old.LastSequence >= terminal.Seq || old.View.Header["state"] != "running" {
		t.Fatal("later terminal leaked into prior capture", old, err)
	}
	fresh, err := ObserveApprovedExecutionContext(ctx, root, binding, approval)
	if err != nil || fresh.TerminalDigest != terminal.Digest || fresh.TerminalState != loop.StateCanceled || fresh.LastSequence != terminal.Seq || fresh.View.Header["state"] != loop.StateCanceled || fresh.CleanupPending {
		t.Fatal("terminal and view disagree", fresh, err)
	}
	if _, err := store.Append(binding.DeliveryID, journal.Record{Kind: loop.KindInterrupted, Detail: json.RawMessage(`{"error":"controlled resumed interruption"}`), Graph: terminal.Graph}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ObserveApprovedExecutionContext(ctx, root, binding, approval)
	if err != nil || resumed.TerminalDigest != "" || resumed.TerminalState != "" || resumed.LastSequence <= terminal.Seq {
		t.Fatal("prior terminal credited after later activity", resumed, err)
	}
}

func TestApprovedExecutionEvidenceRejectsInvalidTerminalAndReportsCleanup(t *testing.T) {
	for _, detail := range []string{`{"state":"invented"}`, `{"state":"canceled","cleanup_pending":true}`, `{"state":"canceled","bookkeeping_pending":true}`} {
		t.Run(detail, func(t *testing.T) {
			root, binding, approval := supervisedProjectionFixture(t)
			store, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(binding.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(detail), Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
				t.Fatal(err)
			}
			out, err := ObserveApprovedExecutionContext(context.Background(), root, binding, approval)
			if detail == `{"state":"invented"}` {
				if !errors.Is(err, ErrArtifactInvalid) {
					t.Fatal(out, err)
				}
			} else if err != nil || !out.CleanupPending || out.TerminalState != loop.StateCanceled {
				t.Fatal("pending cleanup hidden", out, err)
			}
		})
	}
}
