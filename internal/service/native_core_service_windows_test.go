package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/loop"
	"golang.org/x/sys/windows"
)

// A copied test PE is the local selected CLI fixture. Core's public inventory
// adapter invokes its native diagnostic argv before any test flags exist.
func TestMain(m *testing.M) {
	if handled, code := task43CoreLifetimePE(); handled {
		os.Exit(code)
	}
	if strings.EqualFold(filepath.Base(os.Args[0]), "codex.exe") && len(os.Args) > 1 {
		if handled, code := task43CorePreparationPE(); handled {
			os.Exit(code)
		}
		if handled, code := task43CoreEffortPE(); handled {
			os.Exit(code)
		}
		if handled, code := task43CoreReviewGuardPE(); handled {
			os.Exit(code)
		}
		if handled, code := task43CoreReviewPE(); handled {
			os.Exit(code)
		}
		var output string
		switch strings.Join(os.Args[1:], " ") {
		case "--version":
			output = "codex-cli 0.160.0"
		case "debug models", "debug models --bundled":
			output = `{"models":[{"slug":"qa-native"}]}`
		case "doctor --json --summary":
			output = `{"schema_version":1}`
		case "plugin list --json", "plugin marketplace list --json":
			output = `{}`
		}
		if output != "" {
			if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
				os.Exit(90)
			}
			f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-inventory-probes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(91)
			}
			_, err = fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			if err != nil || f.Close() != nil {
				os.Exit(92)
			}
			fmt.Println(output)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func task43CoreSelected(svc *Service, values map[string]nativeBridgeAccount) nativeAccounts {
	selected := nativeAccounts{manager: svc.Session, byCLI: map[string]nativeAccount{}}
	for cli, a := range values {
		selected.byCLI[cli] = nativeAccount{account: domain.Account{ID: "selected", SessionCap: 1}, executable: a.Executable, env: a.Environment, adapter: a.Adapter, members: a.Members}
	}
	return selected
}

func task43CoreCatalogGuard(svc *Service, a domain.PlanApproval, delivery string) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, err := svc.planBinding(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID)
		if err != nil {
			return err
		}
		root, err := batuta.CanonicalWorkspace(base.WorkspaceRoot)
		if err != nil {
			return err
		}
		if root != a.Identity.Binding.CanonicalRoot {
			return domain.ErrBindingConflict
		}
		b := a.Identity.Binding
		b.DeliveryID = delivery
		b.OpenedDigest, err = batuta.ValidateApprovedDelivery(root, b, a)
		if err != nil {
			return err
		}
		return svc.Store.AttachApprovedExecution(ctx, a.ID, a.Identity, b)
	}
}

func task43CoreParentRun(t *testing.T, svc *Service, a domain.PlanApproval, skills string, env []string, values map[string]nativeBridgeAccount, resume string) (string, error, bool) {
	t.Helper()
	job, err := svc.native.admit("m", a.Identity.Binding.CanonicalRoot, batuta.ExecutionDelivery(a))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	ctx, cancel := context.WithTimeout(job.ctx, 35*time.Second)
	defer cancel()
	job.ctx = ctx
	release, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var active atomic.Bool
	guard := task43CoreCatalogGuard(svc, a, job.delivery)
	checkedCapacity := false
	linked := func(ctx context.Context) error {
		if !checkedCapacity {
			unexpected, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
			if err == nil {
				unexpected()
				return errors.New("parent capacity was released before Core effects")
			}
			checkedCapacity = true
		}
		return guard(ctx)
	}
	handled, state, runErr := svc.runNativeWindowsExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, resume, task43CoreSelected(svc, values), skills, env, &active, linked)
	if !handled {
		t.Fatal("actual Windows service protocol branch not selected")
	}
	if runErr == nil && !checkedCapacity {
		t.Fatal("Core never reached actual parent Store guard")
	}
	return state, runErr, active.Load()
}

func TestTask43WindowsNativeCoreParentKeepsReceiptOnResume(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if err != nil || state != loop.StateWaitingInput || !active {
		t.Fatal("actual parent start", state, active, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || first.Watch == nil || first.Watch.PendingQuestion == nil || !first.Bound {
		t.Fatal("actual first approved delivery missing", first, err)
	}
	question := *first.Watch.PendingQuestion
	if err := os.Remove(filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)); err != nil {
		t.Fatal(err)
	}
	answer, err := svc.AnswerQuestion(ctx, question.Target.Answer("approved answer"))
	if err != nil || answer.Effect != domain.AnswerRecorded {
		t.Fatal("actual answer after source removal", answer, err)
	}
	state, err, active = task43CoreParentRun(t, svc, a, skills, env, values, first.Delivery)
	if err != nil || state != loop.StateWaitingInput || !active {
		t.Fatal("actual parent resume", state, active, err)
	}
	next, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || next.Delivery != first.Delivery || next.OpenedDigest != first.OpenedDigest || next.Watch == nil || next.Watch.PendingQuestion == nil || next.Watch.PendingQuestion.Target.Execution <= question.Target.Execution {
		t.Fatal("resume lost original receipt/opening", next, err)
	}
	launches, err := os.ReadFile(filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-launches"))
	if err != nil || string(launches) != "submission\nsubmission\n" {
		t.Fatal("resume duplicated or escaped selected submission", string(launches), err)
	}
	if _, err := os.Stat(filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("helper restored removed mutable source", err)
	}
	lease, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal("parent capacity not released after helper/Release/EOF", err)
	}
	lease()
}

func TestTask43WindowsNativeCoreParentRejectsFreshPlanEdit(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	path := filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)
	changed := append(append([]byte{}, a.Content...), []byte("\n## Decisions\nChanged after approval.\n")...)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if !errors.Is(err, domain.ErrPlanStale) || state != "" || active {
		t.Fatal("parent lost actual fresh approval rejection", state, active, err)
	}
	if _, err := os.Stat(filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-launches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale receipt launched selected worker", err)
	}
	b, err := svc.planBinding(context.Background(), "m", "w")
	if err != nil || b.DeliveryID != "" || b.OpenedDigest != "" {
		t.Fatal("stale receipt attached delivery", b, err)
	}
}

func task43CoreParentImage(t *testing.T, consoled, home, mode string) string {
	t.Helper()
	_, build := task43CoreHome(t)
	source := fmt.Sprintf(`package main
import (
 "encoding/binary"
 "encoding/json"
 "io"
 "os"
 "os/exec"
 "path/filepath"
 "runtime/debug"
 "time"
 _ "github.com/batuta-ai/core/loop"
 "golang.org/x/sys/windows"
)
var image = %q
var record = %q
var mode = %q
func send(v any) {
 b,e:=json.Marshal(v); if e!=nil {os.Exit(71)}
 if binary.Write(os.Stdout,binary.LittleEndian,uint32(len(b)))!=nil {os.Exit(72)}
 if _,e=os.Stdout.Write(b); e!=nil {os.Exit(73)}
}
func receive() {
 var n uint32; if binary.Read(os.Stdin,binary.LittleEndian,&n)!=nil || n==0 || n>4<<20 {os.Exit(74)}
 if _,e:=io.CopyN(io.Discard,os.Stdin,int64(n)); e!=nil {os.Exit(75)}
}
func main() {
 if os.Getenv("OPENAI_API_KEY")!="" || os.Getenv("ANTHROPIC_API_KEY")!="" {os.Exit(90)}
 if os.WriteFile(record,[]byte(os.Getenv("HOME")),0600)!=nil {os.Exit(76)}
 if mode=="relay" || mode=="crash" {
  c:=exec.Command(image,"--native-core"); c.Stdin,c.Stdout,c.Stderr=os.Stdin,os.Stdout,os.Stderr; c.Env=os.Environ()
  if c.Start()!=nil {os.Exit(77)}
  if mode=="crash" {go func(){for {b,e:=os.ReadFile(filepath.Join(filepath.Dir(record),"relay-action"));if e==nil && string(b)=="crash" {os.Exit(37)};time.Sleep(5*time.Millisecond)}}()}
  if e:=c.Wait();e!=nil {if x,ok:=e.(*exec.ExitError);ok {os.Exit(x.ExitCode())};os.Exit(78)}
  return
 }
 h:=map[string]any{"protocol":1,"pid":uint32(os.Getpid())}
 b,ok:=debug.ReadBuildInfo();if !ok {os.Exit(79)}
 for _,d:=range b.Deps {if d.Path=="github.com/batuta-ai/core" {h["core_path"],h["core_version"],h["core_sum"],h["core_replaced"]=d.Path,d.Version,d.Sum,d.Replace!=nil}}
 var born,ended,kernel,user windows.Filetime
 if windows.GetProcessTimes(windows.CurrentProcess(),&born,&ended,&kernel,&user)!=nil {os.Exit(80)}
 h["created"]=uint64(born.HighDateTime)<<32|uint64(born.LowDateTime)
 send(map[string]any{"type":"hello","hello":h});receive()
 switch mode {
 case "duplicate_ready": send(map[string]any{"type":"ready","id":1});receive();send(map[string]any{"type":"ready","id":2})
 case "guard_before_ready":send(map[string]any{"type":"guard","id":1,"guard":map[string]any{}})
 case "nonmonotonic":send(map[string]any{"type":"ready","id":2})
 case "done_before_ready":send(map[string]any{"type":"done","done":map[string]any{"state":"done"}})
 case "unknown":send(map[string]any{"type":"unknown"})
 case "extra":send(map[string]any{"type":"ready","id":1,"extra":true})
 case "empty_state", "unknown_state", "early_cleanup":
  send(map[string]any{"type":"ready","id":1});receive()
  if mode=="early_cleanup" {send(map[string]any{"type":"guard","id":2,"guard":map[string]any{"cleanup":true}})} else {
   state:="";if mode=="unknown_state" {state="unrecognized-state"}
   send(map[string]any{"type":"done","done":map[string]any{"state":state}})
  }
  receive()
  if os.WriteFile(filepath.Join(filepath.Dir(record),"native-accepted-frame"),[]byte(mode),0600)!=nil {os.Exit(85)}
  if mode=="early_cleanup" {
   send(map[string]any{"type":"done","done":map[string]any{"state":"","error":map[string]any{"message":"owned fixture observed premature cleanup response","invalid":true}}});receive()
  }
 case "cleanup":
  e,err:=windows.CreateEvent(nil,1,0,nil);if err!=nil {os.Exit(81)};defer windows.CloseHandle(e)
  var limited windows.Handle
  if windows.DuplicateHandle(windows.CurrentProcess(),e,windows.CurrentProcess(),&limited,windows.EVENT_MODIFY_STATE,false,0)!=nil {os.Exit(82)}
  defer windows.CloseHandle(limited);_,denied:=windows.WaitForSingleObject(limited,0)
  if denied!=windows.ERROR_ACCESS_DENIED {os.Exit(83)}
  send(map[string]any{"type":"done","done":map[string]any{"error":map[string]any{"message":"uncertain native preparation","uncertain":true},"cleanup":map[string]any{"message":denied.Error(),"cleanup":true,"review_cleanup":true,"kernel":[]uint32{uint32(windows.ERROR_ACCESS_DENIED)}}}});receive()
 default:os.Exit(84)
 }
 io.Copy(io.Discard,os.Stdin)
}
`, consoled, filepath.Join(home, "native-parent-home"), mode)
	path := filepath.Join(build, "main.go")
	task43CoreWriteFile(t, path, source)
	image := filepath.Join(build, "parent.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", image, path)
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual native parent fixture build: %v %s", err, out)
	}
	return image
}

func task43CorePreparedRun(t *testing.T, svc *Service, a domain.PlanApproval, values map[string]nativeBridgeAccount, skills string) (*nativeExecution, <-chan struct{}, context.Context) {
	t.Helper()
	job, err := svc.native.admit("m", a.Identity.Binding.CanonicalRoot, batuta.ExecutionDelivery(a))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithTimeout(job.ctx, 45*time.Second)
	t.Cleanup(cancelRun)
	job.ctx = runCtx
	ctx, cancelWait := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancelWait)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer svc.native.finish(job)
		svc.runPreparedNativeExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, "", task43CoreSelected(svc, values), skills)
	}()
	return job, finished, ctx
}

func task43CoreFinishedEvent(t *testing.T, svc *Service, delivery string, failed bool) {
	t.Helper()
	events, err := svc.Store.EventsSince(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "execution.finished" && event.Payload["delivery"] == delivery {
			count++
			if event.Payload["failed"] != failed || failed && event.Payload["state"] == loop.StateDone {
				t.Fatal("actual parent finished event contradicts outcome", event)
			}
		}
	}
	if count != 1 {
		t.Fatal("missing or duplicate actual parent finished event", count)
	}
}

func TestTask43WindowsNativeCorePreparedLifecycle(t *testing.T) {
	consoled := task43CoreImage(t)
	for _, mode := range []string{"relay", "crash"} {
		t.Run(mode, func(t *testing.T) {
			svc, a, skills, _, values := task43CoreApprovedFixture(t)
			home := accounts.ExplicitHome(values["codex"].Environment)
			task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), "hold")
			svc.NativeExecutorExecutable = task43CoreParentImage(t, consoled, home, mode)
			job, finished, ctx := task43CorePreparedRun(t, svc, a, values, skills)
			select {
			case outcome := <-job.ready:
				if outcome.err != nil || outcome.result.Effect != domain.ExecutionStarted || outcome.result.Delivery != job.delivery || outcome.result.OpenedDigest == "" {
					t.Fatal("actual production parent start outcome", outcome)
				}
			case <-ctx.Done():
				t.Fatal("actual parent did not signal start", ctx.Err())
			}
			var tree task43CoreTreeBirth
			for {
				data, err := os.ReadFile(filepath.Join(home, "native-tree-birth.json"))
				if err == nil && json.Unmarshal(data, &tree) == nil {
					break
				}
				select {
				case <-finished:
					t.Fatal("actual held worker did not start")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			root, err := task43CoreWitness(tree.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(root)
			child, err := task43CoreWitness(tree.Child)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(child)
			lease, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
			if err == nil {
				lease()
				t.Fatal("production reservation was released during held Core work")
			}
			data, err := os.ReadFile(filepath.Join(home, "native-parent-home"))
			if err != nil || !filepath.IsAbs(string(data)) || string(data) == home {
				t.Fatal("actual distinct helper HOME not observed", string(data), err)
			}
			helperHome := string(data)
			if info, err := os.Stat(helperHome); err != nil || !info.IsDir() {
				t.Fatal("helper HOME absent during run", err)
			}
			if mode == "relay" {
				shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := svc.ShutdownNative(shutdown); err != nil {
					t.Fatal("actual service shutdown did not join Core", err)
				}
			} else {
				task43CoreWriteFile(t, filepath.Join(home, "relay-action"), "crash")
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("actual parent failed to finalize", ctx.Err())
			}
			for _, h := range []windows.Handle{root, child} {
				if wait, err := windows.WaitForSingleObject(h, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
					t.Fatal("original owned worker survived parent return", wait, err)
				}
			}
			if _, err := os.Stat(helperHome); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("production helper HOME cleanup not joined", err)
			}
			lease, err = svc.Session.ReserveNative(map[string]int{"selected": 1})
			if err != nil {
				t.Fatal("production parent reservation not released", err)
			}
			lease()
			task43CoreFinishedEvent(t, svc, job.delivery, true)
		})
	}
}

func TestTask43WindowsNativeCoreLiveBindingRejection(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	job, err := svc.native.admit("m", a.Identity.Binding.CanonicalRoot, batuta.ExecutionDelivery(a))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	ctx, cancel := context.WithTimeout(job.ctx, 35*time.Second)
	defer cancel()
	job.ctx = ctx
	_, changedRoot := task43CoreHome(t)
	var active atomic.Bool
	result := executionFailure(domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, nil)
	guard := func(ctx context.Context) error {
		if !active.Load() {
			return errors.New("live guard reached before actual READY")
		}
		if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: changedRoot, CreatedAt: domain.Now()}); err != nil {
			return err
		}
		return svc.linkNativeExecution(ctx, job, a, &result)
	}
	_, state, runErr := svc.runNativeWindowsExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, "", task43CoreSelected(svc, values), skills, env, &active, guard)
	if !active.Load() || !errors.Is(runErr, domain.ErrBindingConflict) || state == loop.StateDone {
		t.Fatal("actual live parent rejection/cause lost", active.Load(), state, runErr)
	}
	svc.signalNativeExecutionFailure(job, a, result, active.Load(), runErr)
	select {
	case outcome := <-job.ready:
		if outcome.result.Effect != domain.ExecutionUncertain || outcome.result.Delivery != job.delivery || !errors.Is(outcome.err, domain.ErrBindingConflict) || !errors.Is(outcome.err, domain.ErrExecutionUncertain) {
			t.Fatal("actual production fail mapping lost uncertainty", outcome)
		}
	default:
		t.Fatal("actual production fail did not signal")
	}
	if _, err := os.Stat(filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-launches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected live binding submitted worker", err)
	}
}

func TestTask43WindowsNativeCoreParentRejectsChildFrames(t *testing.T) {
	for _, mode := range []string{"duplicate_ready", "guard_before_ready", "nonmonotonic", "done_before_ready", "unknown", "extra", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			svc, a, skills, _, values := task43CoreApprovedFixture(t)
			home := accounts.ExplicitHome(values["codex"].Environment)
			svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, mode)
			job, finished, ctx := task43CorePreparedRun(t, svc, a, values, skills)
			var outcome nativeStartOutcome
			select {
			case outcome = <-job.ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if outcome.err == nil || outcome.result.Effect == domain.ExecutionStarted {
				t.Fatal("actual parent accepted malformed child traffic", mode, outcome)
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("malformed child did not finalize", ctx.Err())
			}
			if mode == "cleanup" {
				if !errors.Is(outcome.err, session.ErrNativeCleanupUnresolved) || !errors.Is(outcome.err, windows.ERROR_ACCESS_DENIED) {
					t.Fatal("actual non-nil DONE Cleanup cause lost", outcome.err)
				}
				if err := svc.ShutdownNative(context.Background()); !errors.Is(err, session.ErrNativeCleanupUnresolved) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Fatal("native cleanup record lost cause on Shutdown", err)
				}
			}
			data, err := os.ReadFile(filepath.Join(home, "native-parent-home"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(data)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed helper HOME retained", err)
			}
			if _, err := os.Stat(filepath.Join(home, "native-launches")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("malformed child submitted selected worker", err)
			}
			task43CoreFinishedEvent(t, svc, job.delivery, true)
		})
	}
}

func TestTask43WindowsNativeCoreRejectsFinalStateAndCleanupClaim(t *testing.T) {
	for _, mode := range []string{"empty_state", "unknown_state", "early_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			svc, a, skills, _, values := task43CoreApprovedFixture(t)
			home := accounts.ExplicitHome(values["codex"].Environment)
			svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, mode)
			job, finished, ctx := task43CorePreparedRun(t, svc, a, values, skills)
			select {
			case outcome := <-job.ready:
				if !errors.Is(outcome.err, domain.ErrInvalidReference) || outcome.result.Effect == domain.ExecutionStarted {
					t.Fatal("actual parent did not refuse invalid final state/cleanup authority", mode, outcome)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("invalid final frame did not finalize", ctx.Err())
			}
			if _, err := os.Stat(filepath.Join(home, "native-accepted-frame")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("parent acknowledged invalid DONE or answered premature cleanup claim", mode, err)
			}
			if _, err := os.Stat(filepath.Join(home, "native-launches")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid final frame submitted selected worker", err)
			}
			task43CoreFinishedEvent(t, svc, job.delivery, true)
		})
	}
}

func TestTask43WindowsNativeCorePreparedSuccess(t *testing.T) {
	svc, a, skills, _, values := task43CoreApprovedFixture(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	svc.NativeExecutorExecutable = task43CoreParentImage(t, task43CoreImage(t), home, "relay")
	job, finished, ctx := task43CorePreparedRun(t, svc, a, values, skills)
	select {
	case outcome := <-job.ready:
		if outcome.err != nil || outcome.result.Effect != domain.ExecutionStarted || outcome.result.Delivery != job.delivery || outcome.result.OpenedDigest == "" {
			t.Fatal("actual prepared success did not attach/signal original receipt", outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("actual successful prepared continuation did not finalize", ctx.Err())
	}
	events, err := svc.Store.EventsSince(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "execution.finished" && event.Payload["delivery"] == job.delivery {
			count++
			if event.Payload["failed"] != false || event.Payload["state"] != loop.StateWaitingInput {
				t.Fatal("actual prepared success event differs from real Core waiting input", event)
			}
		}
	}
	if count != 1 {
		t.Fatal("actual prepared success missing/duplicate final event", count)
	}
	data, err := os.ReadFile(filepath.Join(home, "native-parent-home"))
	if err != nil || !filepath.IsAbs(string(data)) || string(data) == home {
		t.Fatal("actual distinct helper HOME not observed", string(data), err)
	}
	if _, err := os.Stat(string(data)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful prepared helper HOME retained", err)
	}
	lease, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal("successful production reservation retained", err)
	}
	lease()
	binding, err := svc.planBinding(context.Background(), "m", "w")
	if err != nil || binding.DeliveryID != job.delivery || binding.OpenedDigest == "" {
		t.Fatal("actual prepared success lost parent binding", binding, err)
	}
}
