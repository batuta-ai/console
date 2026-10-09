package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	secure "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func task43CoreHome(t *testing.T) ([]string, string) {
	t.Helper()
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	return env, home
}

func task43CoreWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := secure.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	f, err := secure.CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(f, strings.NewReader(body))
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

func task43CoreImage(t *testing.T) string {
	t.Helper()
	_, home := task43CoreHome(t)
	image := filepath.Join(home, "consoled.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", image, "./cmd/consoled")
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual consoled build: %v %s", err, out)
	}
	return image
}

func task43CoreSend(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	return err
}

func task43CoreReceive(r io.Reader) (map[string]json.RawMessage, error) {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return nil, err
	}
	if size == 0 || size > 4<<20 {
		return nil, fmt.Errorf("invalid actual protocol frame size %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func task43CoreHello(r io.Reader) (windows.Handle, error) {
	frame, err := task43CoreReceive(r)
	if err != nil {
		return 0, fmt.Errorf("actual whole-Core helper hello: %w", err)
	}
	if string(frame["type"]) != `"hello"` {
		return 0, errors.New("helper did not announce its actual Core dependency")
	}
	var h struct {
		Protocol     int    `json:"protocol"`
		CorePath     string `json:"core_path"`
		CoreVersion  string `json:"core_version"`
		CoreSum      string `json:"core_sum"`
		CoreReplaced bool   `json:"core_replaced"`
		PID          uint32 `json:"pid"`
		Created      uint64 `json:"created"`
	}
	if err := json.Unmarshal(frame["hello"], &h); err != nil {
		return 0, err
	}
	if h.Protocol != 1 || h.CorePath != "github.com/batuta-ai/core" || h.CoreVersion != "v1.1.0-beta.49" || h.CoreSum != "h1:GnPVZNM1wbIapKJJoNDc7WQXQNpiWQIWCkkpz1yMWaE=" || h.CoreReplaced || h.PID == 0 || h.Created == 0 {
		return 0, fmt.Errorf("unqualified actual linked Core hello %+v", h)
	}
	witness, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, h.PID)
	if err != nil {
		return 0, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(witness, &created, &exited, &kernel, &user); err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != h.Created {
		windows.CloseHandle(witness)
		return 0, errors.Join(errors.New("original helper birth mismatch"), err)
	}
	return witness, nil
}

func TestTask43WindowsNativeCoreProviderFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-core-provider" {
		return
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		os.Exit(90)
	}
	switch os.Args[4] {
	case "version":
		fmt.Println("codex-cli 0.160.0")
	case "models":
		fmt.Println(`{"models":[{"slug":"qa-native"}]}`)
	case "run":
		file, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-launches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(91)
		}
		fmt.Fprintln(file, "submission")
		if err := file.Close(); err != nil {
			os.Exit(92)
		}
		fmt.Println("BATUTA-QUESTION: which approved output?")
	default:
		os.Exit(93)
	}
	os.Exit(0)
}

func task43CoreApprovedFixture(t *testing.T) (*Service, domain.PlanApproval, string, []string, map[string]nativeBridgeAccount) {
	t.Helper()
	_, root := task43CoreHome(t)
	_, tools := task43CoreHome(t)
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(tools, "codex.exe")
	f, err := secure.CreatePrivateFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(data)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "ambient-must-not-cross-core")
	t.Setenv("ANTHROPIC_API_KEY", "ambient-must-not-cross-core")
	_, skillHome := task43CoreHome(t)
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review", "batuta/adapters"} {
		if err := secure.EnsurePrivateDirectory(filepath.Join(skillHome, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	skills := installedSkillFixture(t, skillHome, "batuta", "batuta-loop", "batuta-review")
	task43CoreWriteFile(t, filepath.Join(skills, "adapters", "codex.md"), "---\nname: codex\nexecutable: codex\nrun: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider run --model {model} \"{brief}\" < /dev/null'\nreadonly: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider review --model {model} \"{prompt}\" < /dev/null'\navailable: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider version'\nmodels: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
	task43CoreWriteFile(t, filepath.Join(skills, "templates", "generic.md"), "# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | qa-native |\n| medium | * | codex | qa-native |\n| high | * | codex | qa-native |\n| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | codex | qa-native |\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "plans", "alpha.md"), "# Plan — Alpha\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n")
	task43CoreWriteFile(t, filepath.Join(root, ".gitignore"), ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "console-native-qa"}, {"config", "user.email", "qa@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: native fixture"}} {
		c := exec.Command(git, args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("native Git fixture: %v %s", err, out)
		}
	}
	svc := setupService(t)
	svc.Session = session.New(root)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: ".batuta/plans/alpha.md"}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := task43CoreHome(t)
	accountEnv, _ := task43CoreHome(t)
	adapter, err := executor.LoadAdapter(skills, "codex")
	if err != nil {
		t.Fatal(err)
	}
	return svc, a, skills, env, map[string]nativeBridgeAccount{"codex": {Executable: cli, Environment: accountEnv, Adapter: adapter, Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: "selected", Maestro: true}}}}
}

func TestTask43WindowsNativeCoreActualQuestionAndRelease(t *testing.T) {
	svc, a, skills, env, selected := task43CoreApprovedFixture(t)
	image := task43CoreImage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var witness windows.Handle
	delivery := batuta.ExecutionDelivery(a)
	var finalState string
	guards, ready := 0, false
	result, err := (session.NativeRunner{Manager: svc.Session, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: a.Identity.Binding.CanonicalRoot}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
		var err error
		witness, err = task43CoreHello(r)
		if err != nil {
			return err
		}
		if err := task43CoreSend(w, map[string]any{"type": "start", "start": map[string]any{"receipt": a, "delivery": delivery, "skills": skills, "environment": env, "accounts": selected}}); err != nil {
			return err
		}
		for {
			frame, err := task43CoreReceive(r)
			if err != nil {
				return err
			}
			switch string(frame["type"]) {
			case `"ready"`:
				if ready {
					return errors.New("duplicate ready")
				}
				ready = true
				if err := task43CoreSend(w, map[string]any{"type": "response", "id": json.RawMessage(frame["id"])}); err != nil {
					return err
				}
			case `"guard"`:
				if !ready {
					return errors.New("effect before parent approval")
				}
				b := a.Identity.Binding
				b.DeliveryID = delivery
				b.OpenedDigest, err = batuta.ValidateApprovedDelivery(b.CanonicalRoot, b, a)
				if err != nil {
					return err
				}
				if err := svc.Store.AttachApprovedExecution(ctx, a.ID, a.Identity, b); err != nil {
					return err
				}
				guards++
				if err := task43CoreSend(w, map[string]any{"type": "response", "id": json.RawMessage(frame["id"])}); err != nil {
					return err
				}
			case `"done"`:
				var done struct {
					State   string           `json:"state"`
					Error   *json.RawMessage `json:"error"`
					Cleanup *json.RawMessage `json:"cleanup"`
				}
				if err := json.Unmarshal(frame["done"], &done); err != nil {
					return err
				}
				if done.Error != nil || done.Cleanup != nil {
					return fmt.Errorf("actual Core failed: %s", frame["done"])
				}
				finalState = done.State
				if err := task43CoreSend(w, map[string]any{"type": "ack"}); err != nil {
					return err
				}
				tail, err := io.ReadAll(r)
				if len(tail) != 0 {
					return errors.New("unframed data after released result")
				}
				return err
			default:
				return fmt.Errorf("unexpected actual helper message %s", frame["type"])
			}
		}
	})
	if witness != 0 {
		defer windows.CloseHandle(witness)
	}
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("real whole-Core run: result=%+v error=%v", result, err)
	}
	if !ready || guards == 0 || finalState != loop.StateWaitingInput {
		t.Fatalf("parent approval/guard/Core state missing ready=%v guards=%d state=%q", ready, guards, finalState)
	}
	if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
		t.Fatal("original helper still alive after Release/ACK/EOF", wait, err)
	}
	view, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || !view.Bound || view.Delivery != delivery || view.OpenedDigest == "" || view.Watch == nil || view.Watch.PendingQuestion == nil {
		t.Fatalf("actual original Store/receipt/question missing %+v %v", view, err)
	}
	launches, err := os.ReadFile(filepath.Join(accounts.ExplicitHome(selected["codex"].Environment), "native-launches"))
	if err != nil || string(launches) != "submission\n" {
		t.Fatalf("selected account submission %q %v", launches, err)
	}
}

func TestTask43WindowsNativeCoreRejectsBeforePreparation(t *testing.T) {
	image := task43CoreImage(t)
	for _, message := range []any{map[string]any{"type": "ack"}, map[string]any{"type": "start", "unexpected": true}, map[string]any{"type": "start", "start": map[string]any{"delivery": "unapproved"}}} {
		env, home := task43CoreHome(t)
		manager := session.New(home)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var witness windows.Handle
		result, runErr := (session.NativeRunner{Manager: manager, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: home}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
			var err error
			witness, err = task43CoreHello(r)
			if err != nil {
				return err
			}
			if err := task43CoreSend(w, message); err != nil {
				return err
			}
			_, err = task43CoreReceive(r)
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("invalid request did not close before preparation: %v", err)
			}
			return nil
		})
		cancel()
		if witness != 0 {
			wait, err := windows.WaitForSingleObject(witness, 0)
			windows.CloseHandle(witness)
			if err != nil || wait != windows.WAIT_OBJECT_0 {
				t.Fatal("rejected helper remained alive", wait, err)
			}
		}
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runErr != nil || result.ExitCode != 1 {
			t.Fatalf("helper rejection result=%+v err=%v", result, runErr)
		}
		if _, err := os.Stat(filepath.Join(home, ".batuta")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unapproved helper prepared Core state", err)
		}
	}
}

func TestTask43WindowsNativeCoreRejectsOutOfOrderResponse(t *testing.T) {
	image := task43CoreImage(t)
	for _, reply := range []map[string]any{{"type": "response", "id": 2}, {"type": "ack"}} {
		svc, a, skills, env, selected := task43CoreApprovedFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		var witness windows.Handle
		result, err := (session.NativeRunner{Manager: svc.Session, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: a.Identity.Binding.CanonicalRoot}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
			var err error
			witness, err = task43CoreHello(r)
			if err != nil {
				return err
			}
			if err := task43CoreSend(w, map[string]any{"type": "start", "start": map[string]any{"receipt": a, "delivery": batuta.ExecutionDelivery(a), "skills": skills, "environment": env, "accounts": selected}}); err != nil {
				return err
			}
			frame, err := task43CoreReceive(r)
			if err != nil {
				return err
			}
			if string(frame["type"]) != `"ready"` {
				return fmt.Errorf("actual Core readiness missing: %s", frame["type"])
			}
			if err := task43CoreSend(w, reply); err != nil {
				return err
			}
			frame, err = task43CoreReceive(r)
			if err != nil {
				return err
			}
			if string(frame["type"]) != `"done"` || !bytes.Contains(frame["done"], []byte(domain.ErrInvalidReference.Error())) {
				return fmt.Errorf("out-of-order response was not refused: %v", frame)
			}
			tail, err := io.ReadAll(r)
			if len(tail) != 0 {
				return errors.New("unframed output after refused response")
			}
			return err
		})
		cancel()
		if witness != 0 {
			wait, err := windows.WaitForSingleObject(witness, 0)
			windows.CloseHandle(witness)
			if err != nil || wait != windows.WAIT_OBJECT_0 {
				t.Fatal("refused original helper stayed alive", wait, err)
			}
		}
		if err != nil || result.ExitCode != 1 || !bytes.Contains(result.Stderr, []byte(domain.ErrInvalidReference.Error())) {
			t.Fatalf("actual out-of-order result=%+v err=%v", result, err)
		}
		if _, err := os.Stat(filepath.Join(accounts.ExplicitHome(selected["codex"].Environment), "native-launches")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("out-of-order response started selected task", err)
		}
		b, err := svc.planBinding(context.Background(), "m", "w")
		if err != nil || b.DeliveryID != "" || b.OpenedDigest != "" {
			t.Fatal("out-of-order response attached parent delivery", b, err)
		}
	}
}

func TestTask43WindowsNativeCoreRejectsMalformedFrame(t *testing.T) {
	image := task43CoreImage(t)
	framed := func(size uint32, payload string) []byte {
		var b bytes.Buffer
		if err := binary.Write(&b, binary.LittleEndian, size); err != nil {
			t.Fatal(err)
		}
		b.WriteString(payload)
		return b.Bytes()
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"zero", framed(0, "")},
		{"oversized", framed((4<<20)+1, "")},
		{"truncated_header", []byte{1, 0}},
		{"truncated_payload", framed(64, "{")},
		{"invalid_json", framed(1, "{")},
		{"trailing_value", framed(uint32(len(`{"type":"ack"}{}`)), `{"type":"ack"}{}`)},
		{"unknown_type", framed(uint32(len(`{"type":"unknown"}`)), `{"type":"unknown"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, home := task43CoreHome(t)
			manager := session.New(home)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var witness windows.Handle
			result, runErr := (session.NativeRunner{Manager: manager, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: home}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
				var err error
				witness, err = task43CoreHello(r)
				if err != nil {
					return err
				}
				if _, err := io.Copy(w, bytes.NewReader(test.raw)); err != nil {
					return err
				}
				if err := w.Close(); err != nil {
					return err
				}
				tail, err := io.ReadAll(r)
				if len(tail) != 0 {
					return errors.New("malformed frame prepared or emitted Core output")
				}
				return err
			})
			if witness != 0 {
				wait, err := windows.WaitForSingleObject(witness, 0)
				windows.CloseHandle(witness)
				if err != nil || wait != windows.WAIT_OBJECT_0 {
					t.Fatal("malformed-frame helper remained alive", wait, err)
				}
			}
			if err := manager.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runErr != nil || result.ExitCode != 1 || len(result.Stderr) == 0 {
				t.Fatalf("malformed frame result=%+v error=%v", result, runErr)
			}
			if _, err := os.Stat(filepath.Join(home, ".batuta")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("malformed frame prepared unapproved Core state", err)
			}
		})
	}
}
