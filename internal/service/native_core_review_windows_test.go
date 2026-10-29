package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
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
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"golang.org/x/sys/windows"
)

func task43CoreReviewPE() (bool, int) {
	if len(os.Args) < 8 || os.Args[2] != "--" || os.Args[3] != "task43-core-provider" {
		return false, 0
	}
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true, 90
	}
	if os.Args[5] != "--model" || os.Args[6] != "qa-native" {
		return true, 94
	}
	log := func(name string) bool {
		f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintln(f, os.Args[4])
		return err == nil && f.Close() == nil
	}
	switch os.Args[4] {
	case "run":
		if !log("native-launches") {
			return true, 91
		}
		if string(mode) == "hold" {
			return true, task43CoreWorkerHold()
		}
		if err := os.WriteFile("out.txt", []byte("approved output\n"), 0600); err != nil {
			return true, 92
		}
		fmt.Println("completed")
	case "review":
		if !log("native-reviews") {
			return true, 91
		}
		if string(mode) == "review-hold" {
			return true, task43CoreWorkerHold()
		}
		if string(mode) == "invalid" {
			fmt.Println("TASK 1: DONE")
			return true, 0
		}
		fmt.Println("<<<FINDINGS")
		if string(mode) == "fix" {
			fmt.Println(`{"severity":"major","kind":"defect","file":"out.txt","line":1,"premise":"synthetic selected fixture defect","path":"out.txt:1 output path","verdict":"fix before ship"}`)
		}
		fmt.Println("FINDINGS>>>")
		fmt.Println("TASK 1: DONE")
	default:
		return true, 93
	}
	return true, 0
}

func task43CorePreparationPE() (bool, int) {
	if len(os.Args) != 2 || os.Args[1] != "--version" {
		return false, 0
	}
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil || string(mode) != "preparation" {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true, 90
	}
	return true, task43CoreWorkerHold()
}

func TestTask43WindowsNativeCoreQualifiedReview(t *testing.T) {
	image := task43CoreImage(t)
	for _, test := range []struct {
		mode, outcome, state string
		code                 int
		cleared              bool
	}{{"valid", "SHIP", loop.StateDone, 0, true}, {"fix", "FIX_BEFORE_SHIP", loop.StateReviewBlocked, 2, false}, {"invalid", "incomplete_coverage", loop.StateReviewBlocked, 3, false}} {
		t.Run(test.mode, func(t *testing.T) {
			svc, a, skills, env, values := task43CoreApprovedFixture(t)
			svc.NativeExecutorExecutable = image
			home := accounts.ExplicitHome(values["codex"].Environment)
			task43CoreReviewFailureDiagnostic(t, a, home)
			task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), test.mode)
			state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
			if err != nil || !active || state != test.state {
				t.Fatal("actual Core reviewed lifecycle", state, active, err)
			}
			opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)}
			observation, err := loop.ObserveSupervision(opts)
			if err != nil || observation.Review == nil {
				t.Fatal("actual review job missing", observation, err)
			}
			job := observation.Review
			if job.State != "reported" || job.Outcome != test.outcome || job.ExitCode != test.code || job.Attempts != 1 || job.ApprovalReceiptID != a.ID || job.PlanContentDigest != a.Identity.Version {
				t.Fatal("actual review verdict/receipt mapping", job, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gate, err := loop.CheckSupervisionGate(ctx, opts)
			if err != nil || !gate.Required || gate.Cleared != test.cleared {
				t.Fatal("actual review gate", gate, err)
			}
			for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
				data, err := os.ReadFile(filepath.Join(job.Artifacts, name))
				if err != nil || len(data) == 0 || fmt.Sprintf("%x", sha256.Sum256(data)) != job.ArtifactDigests[name] {
					t.Fatal("actual review artifact digest", name, err)
				}
				if name == "review.md" {
					verdict := map[int]string{0: "SHIP", 2: "FIX_BEFORE_SHIP", 3: "REWORK"}[test.code]
					if !strings.HasSuffix(string(data), "Verdict: "+verdict+"\n") {
						t.Fatal("actual report verdict differs from exit", string(data))
					}
				}
			}
			data, err := os.ReadFile(filepath.Join(job.Artifacts, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint review.IncrementalState
			if err := json.Unmarshal(data, &checkpoint); err != nil {
				t.Fatal(err)
			}
			if test.mode == "invalid" {
				if checkpoint.Head != job.Base || len(checkpoint.Pending) == 0 {
					t.Fatal("invalid review advanced covered checkpoint", checkpoint)
				}
			} else if checkpoint.Head != job.FinalCommit || len(checkpoint.Pending) != 0 {
				t.Fatal("covered review checkpoint missing", checkpoint)
			}
			reviews, err := os.ReadFile(filepath.Join(home, "native-reviews"))
			if err != nil || !strings.Contains(string(reviews), "review\n") {
				t.Fatal("review escaped selected local PE/HOME", string(reviews), err)
			}
			view, err := svc.ExecutionState(ctx, "m", "w", a.ID)
			if err != nil || view.Watch == nil || view.Watch.Header["state"] != test.state {
				t.Fatal("actual parent watch disagrees with Core", view, err)
			}
			if test.cleared {
				path := filepath.Join(job.Artifacts, "review.md")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\ntampered\n")...), 0600); err != nil {
					t.Fatal(err)
				}
				gate, err := loop.CheckSupervisionGate(ctx, opts)
				if err == nil && gate.Cleared {
					t.Fatal("changed artifact retained completed review gate", gate)
				}
			}
		})
	}
}

func TestTask43WindowsNativeCoreReviewDiagnostic(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	task43CoreWriteFile(t, filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-fixture-mode"), "valid")
	state, err, _ := task43CoreParentRun(t, svc, a, skills, env, values, "")
	t.Logf("Diagnostic only: actual state=%s error=%v", state, err)
	observation, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)})
	if err != nil || observation.Review == nil {
		t.Fatal("diagnostic actual job absent", err)
	}
	data, err := os.ReadFile(filepath.Join(observation.Review.Artifacts, "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Actual report before owned fixture cleanup:\n%s", data)
}

func task43CoreLifetimePE() (bool, int) {
	if len(os.Args) != 2 || os.Args[1] != "--task43-core-hold" {
		return false, 0
	}
	return true, task43CoreHoldEvent()
}

func task43CoreHoldEvent() int {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 95
	}
	defer windows.CloseHandle(event)
	status, err := windows.WaitForSingleObject(event, windows.INFINITE)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return 96
	}
	return 0
}

type task43CoreBirth struct {
	PID     uint32
	Created uint64
}
type task43CoreTreeBirth struct{ Root, Child task43CoreBirth }

func task43CoreBirthOf(handle windows.Handle, pid uint32) (task43CoreBirth, error) {
	var created, exited, kernel, user windows.Filetime
	err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
	return task43CoreBirth{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, err
}

func task43CoreWorkerHold() int {
	child := exec.Command(os.Args[0], "--task43-core-hold")
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return 95
	}
	root, err := task43CoreBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	if err != nil {
		return 95
	}
	var born task43CoreBirth
	var observed error
	if err := child.Process.WithHandle(func(h uintptr) { born, observed = task43CoreBirthOf(windows.Handle(h), uint32(child.Process.Pid)) }); err != nil || observed != nil {
		return 95
	}
	data, err := json.Marshal(task43CoreTreeBirth{Root: root, Child: born})
	if err != nil {
		return 95
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "native-tree-birth.json"), data, 0600); err != nil {
		return 95
	}
	return task43CoreHoldEvent()
}

func task43CoreWitness(birth task43CoreBirth) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, birth.PID)
	if err != nil {
		return 0, err
	}
	actual, err := task43CoreBirthOf(h, birth.PID)
	if err != nil || actual.Created != birth.Created {
		windows.CloseHandle(h)
		return 0, errors.Join(errors.New("original Core fixture process birth mismatch"), err)
	}
	return h, nil
}

func TestTask43WindowsNativeCoreCancelJoinsNestedWorker(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), "hold")
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	foreign := exec.Command(image, "--task43-core-hold")
	foreign.Env, _ = task43CoreHome(t)
	foreign.Env = accounts.RuntimeEnvironment(os.Environ(), foreign.Env)
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
	var siblingBirth task43CoreBirth
	var siblingErr error
	if err := foreign.Process.WithHandle(func(h uintptr) {
		siblingBirth, siblingErr = task43CoreBirthOf(windows.Handle(h), uint32(foreign.Process.Pid))
	}); err != nil || siblingErr != nil {
		t.Fatal(err, siblingErr)
	}
	sibling, err := task43CoreWitness(siblingBirth)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sibling)
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
	type result struct {
		state string
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		_, state, err := svc.runNativeWindowsExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, "", task43CoreSelected(svc, values), skills, env, &active, task43CoreCatalogGuard(svc, a, job.delivery))
		finished <- result{state, err}
	}()
	var tree task43CoreTreeBirth
	for {
		data, readErr := os.ReadFile(filepath.Join(home, "native-tree-birth.json"))
		if readErr == nil {
			if err := json.Unmarshal(data, &tree); err == nil {
				break
			}
		}
		select {
		case result := <-finished:
			t.Fatal("actual worker never produced original birth", result)
		case <-ctx.Done():
			t.Fatal("actual worker birth missing", ctx.Err())
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
	for _, h := range []windows.Handle{root, child, sibling} {
		if wait, err := windows.WaitForSingleObject(h, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("original live objects missing before cancel", wait, err)
		}
	}
	cancel()
	var stopped result
	select {
	case stopped = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("owned helper/nested worker did not drain")
	}
	if !errors.Is(stopped.err, context.Canceled) || stopped.state == loop.StateDone {
		t.Fatal("cancel produced false completion or lost actual cause", stopped)
	}
	for _, h := range []windows.Handle{root, child} {
		if wait, err := windows.WaitForSingleObject(h, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
			t.Fatal("original nested worker still alive at service return", wait, err)
		}
	}
	if wait, err := windows.WaitForSingleObject(sibling, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("whole-Core cancellation touched foreign sibling", wait, err)
	}
	b, err := svc.planBinding(context.Background(), "m", "w")
	if err != nil || b.DeliveryID != job.delivery || b.OpenedDigest == "" {
		t.Fatal("cancellation lost original parent attachment", b, err)
	}
}

func TestTask43WindowsNativeCoreCancelPreparationAndReview(t *testing.T) {
	for _, stage := range []string{"preparation", "review-hold"} {
		t.Run(stage, func(t *testing.T) { task43CoreCancelStage(t, stage) })
	}
}

func task43CoreCancelStage(t *testing.T, stage string) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), stage)
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	foreign := exec.Command(image, "--task43-core-hold")
	foreign.Env, _ = task43CoreHome(t)
	foreign.Env = accounts.RuntimeEnvironment(os.Environ(), foreign.Env)
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
	var siblingBirth task43CoreBirth
	var siblingErr error
	if err := foreign.Process.WithHandle(func(h uintptr) {
		siblingBirth, siblingErr = task43CoreBirthOf(windows.Handle(h), uint32(foreign.Process.Pid))
	}); err != nil || siblingErr != nil {
		t.Fatal(err, siblingErr)
	}
	sibling, err := task43CoreWitness(siblingBirth)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sibling)
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
	type result struct {
		state string
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		_, state, err := svc.runNativeWindowsExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, "", task43CoreSelected(svc, values), skills, env, &active, task43CoreCatalogGuard(svc, a, job.delivery))
		finished <- result{state, err}
	}()
	var tree task43CoreTreeBirth
	for {
		data, readErr := os.ReadFile(filepath.Join(home, "native-tree-birth.json"))
		if readErr == nil {
			if err := json.Unmarshal(data, &tree); err == nil {
				break
			}
		}
		select {
		case result := <-finished:
			t.Fatal("actual worker never produced original birth", result)
		case <-ctx.Done():
			t.Fatal("actual worker birth missing", ctx.Err())
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
	for _, h := range []windows.Handle{root, child, sibling} {
		if wait, err := windows.WaitForSingleObject(h, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("original live objects missing before cancel", wait, err)
		}
	}
	cancel()
	var stopped result
	select {
	case stopped = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("owned helper/nested worker did not drain")
	}
	if !errors.Is(stopped.err, context.Canceled) {
		t.Fatal("cancel produced false completion or lost actual cause", stopped)
	}
	for _, h := range []windows.Handle{root, child} {
		if wait, err := windows.WaitForSingleObject(h, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
			t.Fatal("original nested worker still alive at service return", wait, err)
		}
	}
	if wait, err := windows.WaitForSingleObject(sibling, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("whole-Core cancellation touched foreign sibling", wait, err)
	}
	b, err := svc.planBinding(context.Background(), "m", "w")
	if stage == "preparation" {
		if err != nil || b.DeliveryID != "" || b.OpenedDigest != "" || active.Load() {
			t.Fatal("canceled preparation invented parent attachment", b, active.Load(), err)
		}
	} else {
		if err != nil || b.DeliveryID != job.delivery || b.OpenedDigest == "" {
			t.Fatal("review cancellation lost original parent attachment", b, err)
		}
		options := loop.SupervisionOptions{Workspace: b.CanonicalRoot, Delivery: job.delivery}
		observation, err := loop.ObserveSupervision(options)
		if err != nil || observation.Review == nil || observation.Review.Attempts != 1 || (observation.Review.State != "uncertain" && observation.Review.State != "launching") {
			t.Fatal("review cancellation lost durable uncertain attempt", observation, err)
		}
		gate, err := loop.CheckSupervisionGate(context.Background(), options)
		if err == nil && gate.Cleared {
			t.Fatal("canceled review cleared acceptance gate", gate)
		}
	}
}

func task43CoreEffortPE() (bool, int) {
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil || string(mode) != "effort" || len(os.Args) < 10 || os.Args[3] != "task43-core-provider" {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true, 90
	}
	if os.Args[5] != "--model" || os.Args[6] != "qa-native" || os.Args[7] != "--effort" || os.Args[len(os.Args)-1] == "" {
		return true, 94
	}
	switch os.Args[4] {
	case "run":
		if len(os.Args) != 10 || os.Args[8] != "low" {
			return true, 95
		}
	case "review":
		if len(os.Args) != 11 || os.Args[8] != "high" || os.Args[9] != "--readonly" {
			return true, 96
		}
	default:
		return false, 0
	}
	f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-effort-argv"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return true, 91
	}
	err = json.NewEncoder(f).Encode(os.Args[4 : len(os.Args)-1])
	if err := errors.Join(err, f.Close()); err != nil {
		return true, 92
	}
	if os.Args[4] == "run" {
		if err := os.WriteFile("out.txt", []byte("approved output\n"), 0600); err != nil {
			return true, 93
		}
		fmt.Println("completed")
	} else {
		fmt.Println("<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE")
	}
	return true, 0
}

func TestTask43WindowsNativeCoreBoundEffortAndReadonly(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	path := filepath.Join(skills, "adapters", "codex.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(data), "--model {model}", "--model {model} --effort {effort}")
	body = strings.Replace(body, "review --model {model} --effort {effort}", "review --model {model} --effort {effort} --readonly", 1)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	authorized := values["codex"]
	authorized.Adapter, err = executor.LoadAdapter(skills, "codex")
	if err != nil {
		t.Fatal(err)
	}
	authorized.Members[0].Effort = "low"
	authorized.Members = append(authorized.Members, domain.Member{ID: "reviewer", CLI: "codex", Model: "qa-native", Effort: "high", AccountID: "selected"})
	values["codex"] = authorized
	home := accounts.ExplicitHome(authorized.Environment)
	task43CoreReviewFailureDiagnostic(t, a, home)
	task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), "effort")
	state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if err != nil || !active || state != loop.StateDone {
		t.Fatal("actual bound effort and readonly Core run", state, active, err)
	}
	data, err = os.ReadFile(filepath.Join(home, "native-effort-argv"))
	if err != nil {
		t.Fatal(err)
	}
	runs, reviews := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var argv []string
		if err := json.Unmarshal([]byte(line), &argv); err != nil {
			t.Fatal(err)
		}
		switch strings.Join(argv, " ") {
		case "run --model qa-native --effort low":
			runs++
		case "review --model qa-native --effort high --readonly":
			reviews++
		default:
			t.Fatal("selected PE saw unbound model/effort/readonly argv", argv)
		}
	}
	if runs != 1 || reviews == 0 {
		t.Fatal("missing actual selected run/readonly effort records", runs, reviews)
	}
	gate, err := loop.CheckSupervisionGate(context.Background(), loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)})
	if err != nil || !gate.Cleared {
		t.Fatal("actual effort-bound review did not clear gate", gate, err)
	}
}

func TestTask43WindowsNativeCoreReviewCLIParity(t *testing.T) {
	_, home := task43CoreHome(t)
	image := filepath.Join(home, "published-core55.exe")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", image, "github.com/batuta-ai/core/cmd/batuta")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual pinned public Core CLI build: %v %s", err, out)
	}
	command := exec.Command(image, "review", "-h")
	command.Dir = home
	var publishedOut, publishedHelp bytes.Buffer
	command.Stdout, command.Stderr = &publishedOut, &publishedHelp
	var exit *exec.ExitError
	if err := command.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || publishedOut.Len() != 0 {
		t.Fatal("actual public review help exit contract", err, publishedOut.String())
	}
	var helperOut, helperHelp bytes.Buffer
	if err := (nativeCoreReviewRunner{}).run(context.Background(), home, []string{"-h"}, &helperOut, &helperHelp); !errors.Is(err, flag.ErrHelp) || helperOut.Len() != 0 {
		t.Fatal("actual helper flag-parser help contract", err, helperOut.String())
	}
	const publicMainFooter = "batuta: flag: help requested\n"
	if !strings.HasSuffix(publishedHelp.String(), publicMainFooter) {
		t.Fatal("actual public main help-error footer", publishedHelp.String())
	}
	publicFlags := strings.TrimSuffix(publishedHelp.String(), publicMainFooter)
	if publicFlags != helperHelp.String() {
		t.Fatalf("actual public/helper flags/default descriptions differ\npublic:\n%s\nhelper:\n%s", publicFlags, helperHelp.String())
	}
	for _, expected := range []string{"-base string", "-cohort-files int", "(default 8)", "-full", "-out string", "-parallel int", "(default 1)", "-reviewer string", "-spec string", "-worktree"} {
		if !strings.Contains(helperHelp.String(), expected) {
			t.Fatal("required public review flag/default missing", expected)
		}
	}
	capabilities := exec.Command(image, "capabilities")
	capabilities.Dir = home
	data, err := capabilities.Output()
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		Version  string   `json:"version"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal(data, &public); err != nil || strings.TrimPrefix(public.Version, "v") != "1.1.0-beta.55.1" {
		t.Fatal("actual public Core capability version", public, err)
	}
	found := false
	for _, command := range public.Commands {
		if command == "review" {
			found = true
		}
	}
	if !found {
		t.Fatal("public Core does not advertise review", public)
	}
	t.Logf("Actual public Core55 CLI flags/defaults/help exit1 verified; review capability version=%s. Helper advertises only its implemented review command over the same actual linked version, exercised by the real supervision review cases.", public.Version)
}

func task43CoreReviewGuardPE() (bool, int) {
	if len(os.Args) < 8 || os.Args[2] != "--" || os.Args[3] != "task43-core-provider" || os.Args[4] != "review" {
		return false, 0
	}
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil || string(mode) != "review_tree" && string(mode) != "review_index" {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" || os.Args[5] != "--model" || os.Args[6] != "qa-native" {
		return true, 90
	}
	if err := os.WriteFile("out.txt", []byte("readonly fixture mutated tracked source\n"), 0600); err != nil {
		return true, 91
	}
	if string(mode) == "review_index" {
		command := exec.Command("git", "-c", "core.longpaths=true", "add", "--", "out.txt")
		if out, err := command.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "actual owned index mutation", err, string(out))
			return true, 92
		}
	}
	fmt.Println("<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE")
	return true, 0
}

func TestTask43WindowsNativeCoreReviewTreeAndProofGuards(t *testing.T) {
	image := task43CoreImage(t)
	for _, mode := range []string{"review_tree", "review_index", "proof_tree"} {
		t.Run(mode, func(t *testing.T) {
			proof := "git diff --check"
			if mode == "proof_tree" {
				proof = "printf proof-mutation >> out.txt"
			}
			svc, a, skills, env, values := task43CoreApprovedFixtureWithProof(t, proof)
			home := accounts.ExplicitHome(values["codex"].Environment)
			fixtureMode := mode
			expected := "source tree changed during review"
			if mode == "proof_tree" {
				fixtureMode = "valid"
				expected = "proof of task-1.1 changed the source tree: out.txt"
			}
			task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), fixtureMode)
			svc.NativeExecutorExecutable = image
			state, runErr, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
			if runErr != nil || !active || state != loop.StateReviewBlocked {
				t.Fatal("actual mutation review did not leave blocked Core state", mode, state, active, runErr)
			}
			opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)}
			observation, err := loop.ObserveSupervision(opts)
			if err != nil || observation.Review == nil || observation.Review.State != "failed" || !strings.Contains(observation.Review.Reason, expected) {
				t.Fatal("actual source/proof guard failure not recorded", mode, observation.Review, err)
			}
			job := observation.Review
			if job.Attempts != 1 || len(job.ArtifactDigests) != 0 {
				t.Fatal("source mutation produced accepted artifact receipt", job)
			}
			for _, name := range []string{"review.md", "state.json"} {
				if _, err := os.Stat(filepath.Join(job.Artifacts, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("source mutation published report/checkpoint", name, err)
				}
			}
			checkpoints, err := filepath.Glob(filepath.Join(job.Snapshot, ".batuta", "reviews", "state", "*.json"))
			if err != nil || len(checkpoints) != 0 {
				t.Fatal("source mutation advanced incremental state", checkpoints, err)
			}
			gate, err := loop.CheckSupervisionGate(context.Background(), opts)
			if err != nil || !gate.Required || gate.Cleared {
				t.Fatal("actual mutated review gate cleared", gate, err)
			}
		})
	}
}

type task43CoreReviewEngineInput struct {
	Args        []string `json:"args"`
	Publication bool     `json:"publication"`
}

type task43CoreReviewEngineResult struct {
	ExitCode                int    `json:"exit_code"`
	Error                   string `json:"error"`
	AuthorizedArtifactClean bool   `json:"authorized_artifact_clean"`
}

// This owned test PE invokes the real production review composition, not Core
// Run. The unchanged consoled tests separately qualify the full loop lifecycle.
func task43CoreReviewEnginePE() (bool, error) {
	if !strings.EqualFold(filepath.Base(os.Args[0]), "review-engine.exe") || len(os.Args) != 2 || os.Args[1] != "--native-core" {
		return false, nil
	}
	err := func() error {
		stream := &nativeCoreStream{reader: os.Stdin, writer: os.Stdout}
		hello, err := actualNativeCoreHello()
		if err != nil {
			return err
		}
		if err := stream.send(nativeCoreFrame{Type: "hello", Hello: &hello}); err != nil {
			return err
		}
		frame, err := stream.receive()
		if err != nil || frame.Type != "start" || frame.Start.validate() != nil {
			return errors.Join(domain.ErrInvalidReference, err)
		}
		start := *frame.Start
		shell, err := nativeCoreShellPath()
		if err != nil {
			return err
		}
		path := filepath.Dir(shell) + string(os.PathListSeparator) + os.Getenv("PATH")
		for key, value := range map[string]string{"PATH": path, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.longpaths", "GIT_CONFIG_VALUE_0": "true"} {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
		start.Environment = append(start.Environment, "PATH="+path)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rpc := newNativeCoreRPC(ctx, cancel, stream)
		manager := session.New(accounts.ExplicitHome(start.Environment))
		selected := nativeAccounts{manager: manager, byCLI: map[string]nativeAccount{}}
		var selectedHome string
		for cli, a := range start.Accounts {
			a.Environment = append(a.Environment, "PATH="+path)
			selected.byCLI[cli] = nativeAccount{executable: a.Executable, env: a.Environment, adapter: a.Adapter, members: a.Members}
			selectedHome = accounts.ExplicitHome(a.Environment)
		}
		var input task43CoreReviewEngineInput
		data, err := os.ReadFile(filepath.Join(selectedHome, "native-review-engine-input.json"))
		if err != nil || json.Unmarshal(data, &input) != nil {
			return errors.Join(domain.ErrInvalidReference, err)
		}
		var active atomic.Bool
		commands := nativeCoreCommands{selected: selected, other: session.NativeRunner{Manager: manager, Environment: start.Environment}, rpc: rpc, active: &active}
		if err := rpc.call(ctx, "ready", nil); err != nil {
			return err
		}
		active.Store(true)
		root := start.Receipt.Identity.Binding.CanonicalRoot
		var record task43CoreReviewEngineResult
		var runErr error
		if input.Publication {
			artifact := filepath.Join(root, ".batuta", "reviews", "owned-publication", "review.md")
			gitPath, err := exec.LookPath("git")
			if err != nil {
				return err
			}
			git := publication.GitClient{Executable: gitPath, Runner: nativeCoreReviewPublicationRunner{paths: []string{artifact}, runner: commands}}
			baseline, err := git.WorktreeState(ctx, root)
			if err != nil {
				return err
			}
			tracked, err := nativeCoreTrackedChangeSignatures(root, baseline.HeadSHA)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(artifact, []byte("owned artifact\n"), 0600); err != nil {
				return err
			}
			runErr = nativeCoreReviewSessionError(ctx, git, root, baseline, tracked, nil)
			record.AuthorizedArtifactClean = runErr == nil
			if runErr == nil {
				filename := filepath.Join(root, ".gitignore")
				data, err := os.ReadFile(filename)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filename, append(data, []byte("publication-source-mutation\n")...), 0600); err != nil {
					return err
				}
				runErr = nativeCoreReviewSessionError(ctx, git, root, baseline, tracked, nil)
			}
		} else {
			result, err := (nativeCoreReviewRunner{commands: commands, skills: start.Skills}).Run(ctx, publication.Command{Executable: "console-native-core-review", Directory: root, Args: append([]string{"review"}, input.Args...)})
			record.ExitCode, runErr = result.ExitCode, err
		}
		if runErr != nil {
			record.Error = runErr.Error()
		}
		data, err = json.Marshal(record)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(selectedHome, "native-review-engine-result.json"), data, 0600); err != nil {
			return err
		}
		cleanup := manager.Shutdown(context.WithoutCancel(ctx))
		rpc.final.Store(true)
		if err := stream.send(nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: loop.StateWaitingInput, Error: nativeCoreEncodeError(runErr), Cleanup: nativeCoreEncodeError(cleanup)}}); err != nil {
			return err
		}
		<-rpc.finished
		select {
		case <-rpc.ack:
			return nil
		default:
			return rpc.err
		}
	}()
	return true, err
}

func task43CoreRunReviewEngine(t *testing.T, svc *Service, a domain.PlanApproval, skills string, env []string, values map[string]nativeBridgeAccount, input task43CoreReviewEngineInput) task43CoreReviewEngineResult {
	t.Helper()
	home := accounts.ExplicitHome(values["codex"].Environment)
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(home, "review-engine.exe")
	if err := os.WriteFile(helper, data, 0600); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	task43CoreWriteFile(t, filepath.Join(home, "native-review-engine-input.json"), string(data))
	svc.NativeExecutorExecutable = helper
	job, err := svc.native.admit("m", a.Identity.Binding.CanonicalRoot, batuta.ExecutionDelivery(a))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	ctx, cancel := context.WithTimeout(job.ctx, 45*time.Second)
	defer cancel()
	job.ctx = ctx
	svc.runPreparedNativeExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, job.delivery, task43CoreSelected(svc, values), skills)
	data, err = os.ReadFile(filepath.Join(home, "native-review-engine-result.json"))
	if err != nil {
		t.Fatal("actual contained review composition result missing", err)
	}
	var result task43CoreReviewEngineResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTask43WindowsNativeCoreReviewFlagsAndSpecEdges(t *testing.T) {
	for _, name := range []string{"tracked_only", "worktree_override", "missing_spec_slug", "unavailable_spec_path"} {
		t.Run(name, func(t *testing.T) {
			svc, a, skills, env, values := task43CoreApprovedFixture(t)
			svc.NativeExecutorExecutable = task43CoreImage(t)
			state, err, _ := task43CoreParentRun(t, svc, a, skills, env, values, "")
			if err != nil || state != loop.StateWaitingInput {
				t.Fatal("actual Core opening prerequisite", state, err)
			}
			home := accounts.ExplicitHome(values["codex"].Environment)
			task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), "valid")
			root := a.Identity.Binding.CanonicalRoot
			out := filepath.Join(root, ".batuta", "reviews", "flag-qualification")
			args := []string{"--base", "HEAD", "--full", "--out", out}
			if strings.Contains(name, "spec") {
				spec := "missing-plan"
				if name == "unavailable_spec_path" {
					spec = filepath.Join(root, "unavailable-plan.md")
				}
				args = append(args, "--spec", spec)
			} else {
				path := filepath.Join(root, ".batuta", "routing.md")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(data), "| review | codex | qa-native |", "| review | claude | unbound-native |", 1)), 0600); err != nil {
					t.Fatal(err)
				}
				task43CoreWriteFile(t, filepath.Join(root, "untracked-native.txt"), "actual untracked coverage\n")
				args = append(args, "--reviewer", "codex/qa-native")
				if name == "worktree_override" {
					args = append(args, "--worktree")
				}
			}
			result := task43CoreRunReviewEngine(t, svc, a, skills, env, values, task43CoreReviewEngineInput{Args: args})
			if strings.Contains(name, "spec") {
				if result.ExitCode != -1 || !strings.Contains(result.Error, "load spec") {
					t.Fatal("actual unavailable spec accepted", name, result)
				}
				if _, err := os.Stat(filepath.Join(out, "review.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unavailable spec published review", err)
				}
				return
			}
			if result.ExitCode != 0 || result.Error != "" {
				t.Fatal("actual explicit bound reviewer/flags failed", name, result)
			}
			data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest review.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, file := range manifest.Files {
				if file.Path == "untracked-native.txt" {
					found = file.Untracked && file.Selected
				}
			}
			if found != (name == "worktree_override") {
				t.Fatal("actual worktree flag did not control untracked manifest coverage", name, manifest)
			}
			reviews, err := os.ReadFile(filepath.Join(home, "native-reviews"))
			if err != nil || !strings.Contains(string(reviews), "review\n") {
				t.Fatal("explicit override did not use actual bound readonly PE", err)
			}
		})
	}
}

func TestTask43WindowsNativeCoreReviewPublicationGuard(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	state, err, _ := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if err != nil || state != loop.StateWaitingInput {
		t.Fatal("actual Core opening prerequisite", state, err)
	}
	result := task43CoreRunReviewEngine(t, svc, a, skills, env, values, task43CoreReviewEngineInput{Publication: true})
	if !result.AuthorizedArtifactClean || !strings.Contains(result.Error, "source tree changed during review") || !strings.Contains(result.Error, ".gitignore") {
		t.Fatal("actual publication guard did not exclude only allowed artifact", result)
	}
}
