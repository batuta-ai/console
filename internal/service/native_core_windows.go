package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func actualNativeCoreHello() (nativeCoreHello, error) {
	hello := nativeCoreHello{Protocol: 1, PID: uint32(os.Getpid())}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return hello, domain.ErrExecutionUnavailable
	}
	for _, dep := range build.Deps {
		if dep.Path == "github.com/batuta-ai/core" {
			hello.CorePath, hello.CoreVersion, hello.CoreSum, hello.CoreReplaced = dep.Path, dep.Version, dep.Sum, dep.Replace != nil
		}
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return hello, err
	}
	hello.Created = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if !hello.qualified() {
		return hello, domain.ErrExecutionUnavailable
	}
	return hello, nil
}

func (h nativeCoreHello) qualified() bool {
	return h.Protocol == 1 && h.CorePath == "github.com/batuta-ai/core" && h.CoreVersion == "v1.1.0-beta.55" && h.CoreSum == "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=" && !h.CoreReplaced && h.PID != 0 && h.Created != 0
}

func (s nativeCoreStart) validate() error {
	if s.Receipt.ID == "" || s.Receipt.Identity.Validate() != nil || !filepath.IsAbs(s.Receipt.Identity.Binding.CanonicalRoot) || !filepath.IsAbs(s.Receipt.Identity.Binding.WorkspaceRoot) || len(s.Receipt.Content) == 0 || len(s.Receipt.Content) > domain.MaxPlanBytes || domain.PlanVersion(s.Receipt.Content) != s.Receipt.Identity.Version || !filepath.IsAbs(s.Skills) || !filepath.IsAbs(accounts.ExplicitHome(s.Environment)) || s.Delivery == "" || s.Resume != "" && s.Resume != s.Delivery || len(s.Accounts) == 0 || len(s.Accounts) > 2 {
		return domain.ErrInvalidReference
	}
	if s.Delivery != domain.ExecutionDelivery(s.Receipt.ID, s.Receipt.Identity) {
		return domain.ErrInvalidReference
	}
	for cli, account := range s.Accounts {
		if cli != "codex" && cli != "claude" || !filepath.IsAbs(account.Executable) || !filepath.IsAbs(accounts.ExplicitHome(account.Environment)) || len(account.Members) == 0 || account.Adapter.Executable != cli {
			return domain.ErrInvalidReference
		}
		for _, member := range account.Members {
			if member.CLI != cli || member.AccountID == "" || member.Model == "" || member.Model == executor.DefaultModel {
				return domain.ErrInvalidReference
			}
		}
	}
	return nil
}

func nativeCoreShellPath() (string, error) {
	if shell, err := exec.LookPath("sh"); err == nil && filepath.IsAbs(shell) {
		return shell, nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", errors.Join(domain.ErrExecutionUnavailable, err)
	}
	// Git for Windows installs the actual MSYS shell beside its native cmd
	// directory. Resolve that executable; neither cmd.exe nor a script is a
	// substitute for the Core profile's POSIX gate language.
	shell, err := exec.LookPath(filepath.Join(filepath.Dir(filepath.Dir(git)), "usr", "bin", "sh.exe"))
	if err != nil {
		return "", errors.Join(domain.ErrExecutionUnavailable, err)
	}
	return shell, nil
}

type nativeCoreCommands struct {
	selected nativeAccounts
	other    session.NativeRunner
	rpc      *nativeCoreRPC
	active   *atomic.Bool
	readonly bool
}

func (r nativeCoreCommands) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	failed := publication.CommandResult{ExitCode: -1}
	var selected *nativeAccount
	for _, account := range r.selected.byCLI {
		if strings.EqualFold(filepath.Clean(command.Executable), filepath.Clean(account.executable)) {
			// Authorization uses the admitted spelling as well as its argv.
			command.Executable = account.executable
			selected = &account
			break
		}
	}
	if selected != nil {
		if err := r.rpc.master.Err(); err != nil {
			return failed, err
		}
		check := selected.authorizeCommand
		if r.readonly {
			check = selected.authorizeReadonlyCommand
		}
		if err := check(command); err != nil {
			return failed, err
		}
	} else {
		base := strings.ToLower(filepath.Base(command.Executable))
		if base == "codex" || base == "codex.exe" || base == "codex.cmd" || base == "claude" || base == "claude.exe" || base == "claude.cmd" {
			return failed, domain.ErrInvalidReference
		}
		var err error
		if base == "git" || base == "git.exe" {
			_, err = session.NativeEnvironment(nil, command.Environment)
			// Keep the argv override after command-local GIT_CONFIG_PARAMETERS.
			command.Args = append([]string{"-c", "core.longpaths=true"}, command.Args...)
		} else {
			command.Environment, err = nativeCoreGitEnvironment(command.Environment)
		}
		if err != nil {
			return failed, err
		}
	}
	if r.active.Load() {
		request := &nativeCoreGuard{Cleanup: r.rpc.master.Err() != nil && ctx.Err() == nil, Executable: command.Executable, Directory: command.Directory, Args: command.Args, Readonly: r.readonly}
		if err := r.rpc.call(ctx, "guard", request); err != nil {
			return failed, err
		}
	}
	if selected != nil {
		return (session.NativeRunner{Manager: r.selected.manager, Environment: selected.env}).Run(ctx, command)
	}
	return r.other.Run(ctx, command)
}

func nativeCoreGitEnvironment(env []string) ([]string, error) {
	// Validate the original pairs before adding an entry, so inherited or
	// missing configuration cannot be hidden by the controlled setting.
	validated, err := session.NativeEnvironment(nil, env)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, entry := range validated {
		if value, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			count, err = strconv.Atoi(value)
			if err != nil || count >= 256 {
				return nil, errors.New("native Git configuration has no room for long paths")
			}
		}
	}
	// Only command-local entries cross this boundary. The validated environment
	// also contains runtime inheritance and must not become command-local input.
	return append(append([]string{}, env...),
		"GIT_CONFIG_COUNT="+strconv.Itoa(count+1),
		"GIT_CONFIG_KEY_"+strconv.Itoa(count)+"=core.longpaths",
		"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"=true"), nil
}

// RunNativeCore is the private consoled stdio entry point. The caller launches
// the entire image in the original owned Job, before any Core preparation.
func RunNativeCore(ctx context.Context, input io.Reader, output io.Writer) error {
	observation := newNativeCoreObservation(true)
	defer observation.close()
	stream := &nativeCoreStream{reader: input, writer: output, observer: observation.observer()}
	hello, err := actualNativeCoreHello()
	if err != nil {
		return err
	}
	if err := stream.send(nativeCoreFrame{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	frame, err := stream.receive()
	if err != nil {
		return err
	}
	if frame.Type != "start" || frame.Start.validate() != nil {
		return domain.ErrInvalidReference
	}
	start := *frame.Start
	shell, err := nativeCoreShellPath()
	if err != nil {
		return err
	}
	path := filepath.Dir(shell) + string(os.PathListSeparator) + os.Getenv("PATH")
	if err := os.Setenv("PATH", path); err != nil {
		return err
	}
	// Public Core review also invokes Git directly. Scope long-path support to
	// this contained image; injected commands still close inherited Git config
	// and receive controlled long-path support at the command boundary.
	for key, value := range map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.longpaths", "GIT_CONFIG_VALUE_0": "true"} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	start.Environment = append(start.Environment, "PATH="+path)
	for cli, a := range start.Accounts {
		a.Environment = append(a.Environment, "PATH="+path)
		start.Accounts[cli] = a
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rpc := newNativeCoreRPC(ctx, cancel, stream)
	manager := session.New(accounts.ExplicitHome(start.Environment))
	selected := nativeAccounts{manager: manager, byCLI: map[string]nativeAccount{}}
	for cli, a := range start.Accounts {
		selected.byCLI[cli] = nativeAccount{executable: a.Executable, env: a.Environment, adapter: a.Adapter, members: a.Members}
	}
	var active atomic.Bool
	commands := nativeCoreCommands{selected: selected, other: session.NativeRunner{Manager: manager, Environment: start.Environment}, rpc: rpc, active: &active}
	guard := func(callback context.Context) error {
		return rpc.call(callback, "guard", &nativeCoreGuard{Cleanup: ctx.Err() != nil && callback.Err() == nil})
	}
	transport := &executor.TransportBackend{Mode: "cli"}
	opts := loop.Options{
		Workspace: start.Receipt.Identity.Binding.CanonicalRoot, Skills: start.Skills, Plan: start.Receipt.Identity.Binding.PlanPath,
		ApprovedSnapshot: &loop.ApprovedPlanSnapshot{Slug: start.Receipt.Identity.Binding.PlanSlug, Path: start.Receipt.Identity.Binding.PlanPath, ReceiptID: start.Receipt.ID, ContentDigest: start.Receipt.Identity.Version, Content: start.Receipt.Content},
		DeliveryID:       start.Delivery, Parallel: 1, KeepWorktrees: true, Supervision: true,
		Runner: commands, Environment: []string{"GIT_CONFIG_COUNT=0"}, Transport: transport, VerifierTransport: transport,
		Inventory: func(ctx context.Context) (inventory.InventorySnapshot, error) {
			return selected.inventory(ctx, start.Receipt.Identity.Binding.CanonicalRoot)
		},
		Stdout:     &nativeOpeningWriter{ctx: ctx, guard: guard, active: &active},
		Supervisor: &loop.SuperviseOptions{Interval: 200 * time.Millisecond, Review: &loop.SupervisionReviewOptions{Executable: "console-native-core-review", Skills: start.Skills, Runner: nativeCoreReviewRunner{commands: commands, skills: start.Skills}}},
	}
	var runner *loop.Runner
	if start.Resume != "" {
		opts.DeliveryID, opts.Resume = "", start.Resume
		runner, err = loop.Resume(ctx, opts)
	} else {
		runner, err = loop.New(ctx, opts)
	}
	state := ""
	if err == nil {
		err = authorizeNativePreview(runner, selected)
	}
	if err == nil {
		err = rpc.call(ctx, "ready", nil)
	}
	if err == nil {
		active.Store(true)
		stream.observer.Observe("core.run.begin", "", 0, nil, 0)
		state, err = runner.Run(ctx)
		stream.observer.Observe("core.run.end", "", 0, err, 0)
	}
	var cleanup error
	if runner != nil {
		stream.observer.Observe("core.release.begin", "", 0, nil, 0)
		cleanup = runner.Release()
		stream.observer.Observe("core.release.end", "", 0, cleanup, 0)
	}
	stream.observer.Observe("core.shutdown.begin", "", 0, nil, 0)
	shutdownErr := manager.Shutdown(context.WithoutCancel(ctx))
	stream.observer.Observe("core.shutdown.end", "", 0, shutdownErr, 0)
	cleanup = errors.Join(cleanup, shutdownErr)
	if cleanup != nil {
		cleanup = errors.Join(session.ErrNativeCleanupUnresolved, cleanup)
	}
	rpc.final.Store(true)
	done := &nativeCoreDone{State: state, Error: nativeCoreEncodeError(err), Cleanup: nativeCoreEncodeError(cleanup)}
	stream.observer.Observe("helper.done.begin", "done", 0, nil, 0)
	if err := stream.send(nativeCoreFrame{Type: "done", Done: done}); err != nil {
		stream.observer.Observe("helper.done.end", "done", 0, err, 0)
		return err
	}
	stream.observer.Observe("helper.done.end", "done", 0, nil, 0)
	stream.observer.Observe("rpc.join.begin", "", 0, nil, 0)
	<-rpc.finished
	stream.observer.Observe("rpc.join.end", "", 0, rpc.err, 0)
	select {
	case <-rpc.ack:
		return nil
	default:
		return errors.Join(rpc.err, io.ErrUnexpectedEOF)
	}
}

func (s *Service) runNativeWindowsExecution(job *nativeExecution, req domain.ExecutionRequest, receipt domain.PlanApproval, resume string, selected nativeAccounts, skills string, env []string, active *atomic.Bool, guard func(context.Context) error) (bool, string, error) {
	helper := s.NativeExecutorExecutable
	if helper == "" {
		var err error
		helper, err = os.Executable()
		if err != nil {
			return true, "", err
		}
	}
	helper, err := exec.LookPath(helper)
	if err != nil || !filepath.IsAbs(helper) {
		return true, "", errors.Join(domain.ErrExecutionUnavailable, err)
	}
	start := nativeCoreStart{Receipt: receipt, Delivery: job.delivery, Resume: resume, Skills: skills, Environment: env, Accounts: map[string]nativeBridgeAccount{}}
	for cli, a := range selected.byCLI {
		start.Accounts[cli] = nativeBridgeAccount{Executable: a.executable, Environment: a.env, Adapter: a.adapter, Members: a.members}
	}
	if err := start.validate(); err != nil {
		return true, "", err
	}
	var state string
	var outcome, parentCause, cleanup error
	completed := false
	observation := newNativeCoreObservation(false)
	defer observation.close()
	observer := observation.observer()
	result, exchangeErr := (session.NativeRunner{Manager: s.Session, Environment: env}).Exchange(observation.context(job.ctx), publication.Command{Executable: helper, Args: []string{"--native-core"}, Directory: receipt.Identity.Binding.CanonicalRoot}, func(callback context.Context, input io.Reader, output io.WriteCloser) error {
		stream := &nativeCoreStream{reader: input, writer: output, observer: observation.observer()}
		frame, err := stream.receive()
		if err != nil {
			return err
		}
		if frame.Type != "hello" || !frame.Hello.qualified() {
			return domain.ErrExecutionUnavailable
		}
		observer.Observe("parent.hello.birth", "hello", uint64(frame.Hello.PID), nil, int64(frame.Hello.Created))
		// The diagnostic birth fields never confer process control authority.
		if err := stream.send(nativeCoreFrame{Type: "start", Start: &start}); err != nil {
			return err
		}
		stopCancel, cancelJoined := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(cancelJoined)
			select {
			case <-callback.Done():
				observer.Observe("parent.cancel.watcher", "", 0, callback.Err(), 0)
				_ = stream.send(nativeCoreFrame{Type: "cancel"})
			case <-stopCancel:
			}
		}()
		defer func() {
			close(stopCancel)
			<-cancelJoined
			observer.Observe("parent.cancel.joined", "", 0, nil, 0)
		}()
		var last uint64
		ready := false
		for {
			frame, err := stream.receive()
			if err != nil {
				observer.Observe("parent.receive.error", "", 0, err, 0)
				return err
			}
			switch frame.Type {
			case "ready", "guard":
				if frame.ID != last+1 {
					return domain.ErrInvalidReference
				}
				last = frame.ID
				var response error
				if frame.Type == "ready" {
					if ready {
						return domain.ErrInvalidReference
					}
					if resume == "" {
						_, response = s.executionApproval(job.ctx, req)
					} else {
						response = job.ctx.Err()
					}
					if response == nil {
						active.Store(true)
						ready = true
					}
				} else {
					if !ready {
						return domain.ErrInvalidReference
					}
					request := frame.Guard
					if request.Cleanup && job.ctx.Err() == nil {
						return domain.ErrInvalidReference
					}
					checkCtx := job.ctx
					if request.Cleanup {
						checkCtx = context.WithoutCancel(job.ctx)
					}
					for _, a := range selected.byCLI {
						if request.Executable == a.executable {
							command := publication.Command{Executable: request.Executable, Directory: request.Directory, Args: request.Args}
							if request.Cleanup {
								response = context.Canceled
							} else if request.Readonly {
								response = a.authorizeReadonlyCommand(command)
							} else {
								response = a.authorizeCommand(command)
							}
						}
					}
					if response == nil {
						response = guard(checkCtx)
					}
				}
				parentCause = errors.Join(parentCause, response)
				if err := stream.send(nativeCoreFrame{Type: "response", ID: frame.ID, Error: nativeCoreEncodeError(response)}); err != nil {
					return err
				}
			case "done":
				if !ready && frame.Done.Error == nil {
					return domain.ErrInvalidReference
				}
				if frame.Done.Error == nil {
					switch frame.Done.State {
					case loop.StateDone, loop.StateBlocked, loop.StateWaitingInput, loop.StateWaitingPlan, loop.StateCanceled, loop.StateAbandoned, loop.StateReviewBlocked:
					default:
						return domain.ErrInvalidReference
					}
				}
				observer.Observe("parent.done.accepted", "done", 0, nil, 0)
				completed = true
				state, outcome, cleanup = frame.Done.State, frame.Done.Error.cause(), frame.Done.Cleanup.cause()
				observer.Observe("parent.ack.begin", "ack", 0, nil, 0)
				if err := stream.send(nativeCoreFrame{Type: "ack"}); err != nil {
					observer.Observe("parent.ack.end", "ack", 0, err, 0)
					return err
				}
				observer.Observe("parent.ack.end", "ack", 0, nil, 0)
				observer.Observe("parent.stdin.close.begin", "", 0, nil, 0)
				closeErr := output.Close()
				observer.Observe("parent.stdin.close.end", "", 0, closeErr, 0)
				if err := closeErr; err != nil {
					return err
				}
				observer.Observe("parent.stdout.drain.begin", "", 0, nil, 0)
				tail, err := io.ReadAll(input)
				observer.Observe("parent.stdout.drain.end", "", 0, err, int64(len(tail)))
				if len(tail) != 0 {
					return domain.ErrInvalidReference
				}
				return err
			default:
				return domain.ErrInvalidReference
			}
		}
	})
	observer.Observe("parent.exchange.return", "", 0, exchangeErr, int64(result.ExitCode))
	if result.ExitCode != 0 && exchangeErr == nil {
		exchangeErr = fmt.Errorf("native Core helper exited %d", result.ExitCode)
	}
	if !completed && exchangeErr != nil {
		if !active.Load() && job.ctx.Err() == nil && !errors.Is(exchangeErr, context.Canceled) && (errors.Is(exchangeErr, io.EOF) || errors.Is(exchangeErr, io.ErrUnexpectedEOF)) {
			exchangeErr = errors.Join(domain.ErrExecutionUnavailable, exchangeErr)
		}
		if diagnostic := strings.TrimSpace(string(result.Stderr)); diagnostic != "" {
			if len(diagnostic) > 4096 {
				diagnostic = strings.ToValidUTF8(diagnostic[:4096], "")
			}
			exchangeErr = errors.Join(exchangeErr, fmt.Errorf("native Core helper: %s", diagnostic))
		}
	}
	if cleanup != nil || errors.Is(exchangeErr, session.ErrNativeCleanupUnresolved) {
		s.recordExecutionCleanup(job, errors.Join(cleanup, exchangeErr))
	}
	return true, state, errors.Join(parentCause, outcome, cleanup, exchangeErr)
}
