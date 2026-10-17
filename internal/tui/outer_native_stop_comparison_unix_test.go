//go:build darwin || linux

package tui

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

// Captured from the real outer-Console Codex SHIP journey. The age is an
// observation-time metric; every other captured field remains authoritative.
const outerNativeCapturedSHIP = `{"gate":{"required":true,"cleared":true,"delivery":"console-56284f30171e0b37ea9892ce185eb09b29558cdb030ebcfb031a5a07bb1fb8bb","review_id":"f8a3eb46d1a921b99a6420e5020a6ec0d4a2831a6fb6693ee8085e59bfd680e5","evidence_digest":"sha256:49cdcdb401cbfa39def5beb3221534971f327ae43816951fde0c2601dfd1572f","reason":"complete SHIP evidence clears progression only"},"target":{"mission_id":"msn_4c28a557aa9bdb9e","binding":{"workspace_id":"ws_ce4d0103dcfebb30","workspace_root":"/Volumes/Home/francisross/Projects/batuta/console/.batuta/runs/2026-10-14-windows-beta1/native-stop-outer-pty-retry-root-first/tmp/console-outer-native-2272467805/home/browser-roots/OwnedPublicFlowHost561549531-002","canonical_root":"/Volumes/Home/francisross/Projects/batuta/console/.batuta/runs/2026-10-14-windows-beta1/native-stop-outer-pty-retry-root-first/tmp/console-outer-native-2272467805/home/browser-roots/OwnedPublicFlowHost561549531-002","plan_path":".batuta/plans/alpha.md","plan_slug":"alpha","plan_digest":"a6bf7d6884bbb900b55576ba6d3874022c4deae230c0c4d4019949405a7398f1"},"version":"ef183e3507a9f781b9fa3d896a4b51df0f4cb4fd9e9c0076ceeaa0f7ba5d0b3e"},"approval_id":"approval58a3fc168c9d8312","delivery":"console-56284f30171e0b37ea9892ce185eb09b29558cdb030ebcfb031a5a07bb1fb8bb","opened_digest":"sha256:5b589e8b6134d460c17088fd378f969f5b9288ca1f70b0ab9086f5def9cbaad2","active":false,"bound":true,"watch":{"progression":{"required":true,"cleared":true,"delivery":"console-56284f30171e0b37ea9892ce185eb09b29558cdb030ebcfb031a5a07bb1fb8bb","review_id":"f8a3eb46d1a921b99a6420e5020a6ec0d4a2831a6fb6693ee8085e59bfd680e5","evidence_digest":"sha256:49cdcdb401cbfa39def5beb3221534971f327ae43816951fde0c2601dfd1572f","reason":"complete SHIP evidence clears progression only"},"mission_id":"msn_4c28a557aa9bdb9e","workspace_id":"ws_ce4d0103dcfebb30","scope":"mission","source":"current","api":"v0","workspace":"/Volumes/Home/francisross/Projects/batuta/console/.batuta/runs/2026-10-14-windows-beta1/native-stop-outer-pty-retry-root-first/tmp/console-outer-native-2272467805/home/browser-roots/OwnedPublicFlowHost561549531-002","delivery":"console-56284f30171e0b37ea9892ce185eb09b29558cdb030ebcfb031a5a07bb1fb8bb","header":{"branch":"main","delivery":"alpha","head":"82e15b62d44683e2e552f53978efb31801734569","loops":0,"phase":0,"phase_title":"","presence":"none","project":"OwnedPublicFlowHost561549531-002","roadmap":"","state":"done"},"attention":{"hint":"","kind":"none","task":"","text":""},"context":{"escalations":0,"executor":"","model":"","pid":0,"reasoning":"","retries":0,"sessions":1},"progress":{"criterion":0,"criterion_title":"","criterion_total":0,"tasks_done":1,"tasks_total":1,"waves_done":1,"waves_total":1},"detail":{"attempt":1,"criterion":0,"criterion_state":"","last":"worktree_snapshotted task_1 execution=1 sha=9499364306c90df54b3a97fd5bba2a270bf38990 ref=refs/batuta/parked/alpha/task-1-e1","log_path":".batuta/runs/2026-10-17-alpha-task-1-e1.out.log","question":"","reason":"","task":"task_1","title":"Create output","worktree":"/Volumes/Home/francisross/Projects/batuta/console/.batuta/runs/2026-10-14-windows-beta1/native-stop-outer-pty-retry-root-first/tmp/console-outer-native-2272467805/home/browser-roots/OwnedPublicFlowHost561549531-002/.batuta/worktrees/alpha-task-1-e1"},"waves":[{"base":"953822387f1e545ea56aef270cfd89946dbff0d2","dependencies":null,"done":1,"integrated":"82e15b62d44683e2e552f53978efb31801734569","number":1,"rows":[{"attempt":1,"commit":"82e15b62d44683e2e552f53978efb31801734569","gates":["pass","pass","pass","pass"],"state":"integrated","task":"task_1","title":"Create output"}],"state":"integrated","total":1}],"health":{"journal_age_ns":3387881000,"last_error":"","tree_dirty":null},"plans":[{"name":"alpha","status":"proposed"}],"available":true}}`

func outerNativeCapturedState(t *testing.T) domain.ExecutionState {
	t.Helper()
	var state domain.ExecutionState
	if err := json.Unmarshal([]byte(outerNativeCapturedSHIP), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestOuterNativeExecutionObservationComparison(t *testing.T) {
	t.Run("elapsed-observation-age-only", func(t *testing.T) {
		visible, actual := outerNativeCapturedState(t), outerNativeCapturedState(t)
		beforeVisible, beforeActual := outerNativeCapturedState(t), outerNativeCapturedState(t)
		actual.Watch.Health["journal_age_ns"] = float64(4011994000)
		beforeActual.Watch.Health["journal_age_ns"] = float64(4011994000)
		if !outerNativeExecutionEqual(visible, actual) {
			t.Fatal("observation age alone changed execution semantics")
		}
		if !reflect.DeepEqual(visible, beforeVisible) || !reflect.DeepEqual(actual, beforeActual) {
			t.Fatal("comparison mutated retained evidence")
		}
	})
	t.Run("every-other-captured-field-is-strict", func(t *testing.T) {
		var original map[string]any
		if err := json.Unmarshal([]byte(outerNativeCapturedSHIP), &original); err != nil {
			t.Fatal(err)
		}
		var walk func(any, []string)
		walk = func(value any, path []string) {
			switch node := value.(type) {
			case map[string]any:
				for key, child := range node {
					walk(child, append(append([]string(nil), path...), key))
				}
			default:
				if reflect.DeepEqual(path, []string{"watch", "health", "journal_age_ns"}) {
					return
				}
				name := ""
				for _, key := range path {
					name += "/" + key
				}
				t.Run(name, func(t *testing.T) {
					var changed map[string]any
					if err := json.Unmarshal([]byte(outerNativeCapturedSHIP), &changed); err != nil {
						t.Fatal(err)
					}
					at := changed
					for _, key := range path[:len(path)-1] {
						at = at[key].(map[string]any)
					}
					key := path[len(path)-1]
					switch v := value.(type) {
					case string:
						at[key] = v + "-changed"
					case bool:
						at[key] = !v
					case float64:
						at[key] = v + 1
					default:
						delete(at, key)
					}
					encoded, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					var actual domain.ExecutionState
					if err = json.Unmarshal(encoded, &actual); err != nil {
						t.Fatal(err)
					}
					if outerNativeExecutionEqual(outerNativeCapturedState(t), actual) {
						t.Fatal("non-age field change accepted", name)
					}
				})
			}
		}
		walk(original, nil)
	})
	for _, tc := range []struct {
		name   string
		value  any
		remove bool
	}{
		{name: "missing", remove: true}, {name: "negative", value: float64(-1)},
		{name: "string", value: "4011994000"}, {name: "nil", value: nil},
		{name: "fraction", value: float64(0.5)}, {name: "nan", value: math.NaN()},
		{name: "infinity", value: math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for side := 0; side < 2; side++ {
				visible, actual := outerNativeCapturedState(t), outerNativeCapturedState(t)
				target := visible.Watch
				if side == 1 {
					target = actual.Watch
				}
				if tc.remove {
					delete(target.Health, "journal_age_ns")
				} else {
					target.Health["journal_age_ns"] = tc.value
				}
				if outerNativeExecutionEqual(visible, actual) {
					t.Fatal("invalid age accepted", side)
				}
			}
		})
	}
	t.Run("identical-invalid-age", func(t *testing.T) {
		visible, actual := outerNativeCapturedState(t), outerNativeCapturedState(t)
		delete(visible.Watch.Health, "journal_age_ns")
		delete(actual.Watch.Health, "journal_age_ns")
		if outerNativeExecutionEqual(visible, actual) {
			t.Fatal("missing age on both sides accepted")
		}
	})
}
