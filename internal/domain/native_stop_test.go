package domain

import (
	"strings"
	"testing"
)

func TestExecutionStopAuthorityRequiresEveryExactComponent(t *testing.T) {
	target := PlanIdentity{MissionID: "m", Binding: MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/w", CanonicalRoot: "/w", PlanPath: ".batuta/plans/a.md", PlanSlug: "a", PlanDigest: "tasks"}, Version: PlanVersion([]byte("approved"))}
	req := ExecutionStopRequest{Target: target, ApprovalID: "receipt", Delivery: ExecutionDelivery("receipt", target), RunID: strings.Repeat("a", 64)}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ExecutionStopRequest){
		func(r *ExecutionStopRequest) { r.RunID = "" }, func(r *ExecutionStopRequest) { r.RunID = strings.Repeat("A", 64) },
		func(r *ExecutionStopRequest) { r.ApprovalID = "" }, func(r *ExecutionStopRequest) { r.Delivery = "foreign" },
		func(r *ExecutionStopRequest) { r.Target.MissionID = "other" }, func(r *ExecutionStopRequest) { r.Target.Binding.DeliveryID = r.Delivery },
	} {
		bad := req
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
