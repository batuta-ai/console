# TUI interactive terminal contract

This is the implementation contract for Task 29. It selects terminal emulation and defines transport, ownership and verification requirements. It does not qualify a provider, platform or release. Live Codex and Claude qualification belongs to Tasks 39, 40 and 44; native Windows PTY qualification belongs to Task 43.

## Choice and verified versions

Keep Bubble Tea as the application host and use `github.com/charmbracelet/x/vt` for the child terminal's screen state. Feed its cell screen into a bounded terminal region in the Bubble Tea view. Do not render the PTY stream as accumulated ANSI text or allow child escape sequences to control the application's outer terminal.

Versions checked against the official Go module proxy on 2026-10-06:

| Module | Selected version | Role and minimum Go version |
| --- | --- | --- |
| `charm.land/bubbletea/v2` | `v2.0.10` | Existing application host; Go 1.26.0 |
| `github.com/charmbracelet/x/vt` | `v0.0.0-20261004011457-ad85c59fdf4e` | Child screen emulator; Go 1.24.2 |
| `github.com/charmbracelet/x/ansi` | `v0.11.8` | Existing parser, sequences and width utilities; Go 1.24.2 |

The VT version resolves to immutable source `ad85c59fdf4ebe61d54a4d316d84bf16838beb55`. Task 29 pins that version and its checksum, preserves the Console's Go 1.27.1 toolchain and existing Bubble Tea/Ultraviolet/ANSI versions, and reviews the resolved graph without downgrading newer compatible dependencies. A pseudo-version is an identified upstream snapshot, not a stable release or compatibility certificate.

| Available approach | What it supplies | Decision |
| --- | --- | --- |
| Current raw PTY output tail | A byte log with a fixed tail | Cannot represent cursor motion, erase, alternate screen or cell boundaries correctly |
| Bubble Tea alone | Application rendering, input and outer-terminal lifecycle | Retain it; it does not replace a child terminal screen state machine |
| ANSI parsing/width utilities alone | Sequence parsing and width calculation | Retain utilities; implementing a second screen emulator in Console would add avoidable protocol state |
| Dedicated VT emulator | Cell screen, parser, cursor, main/alternate screens, input encoding and scrollback APIs | Select it behind a Console-owned adapter; behavior and lifecycle still require fixtures |

Official sources: [VT module metadata](https://proxy.golang.org/github.com/charmbracelet/x/vt/@v/v0.0.0-20261004011457-ad85c59fdf4e.info), [VT dependency declarations](https://proxy.golang.org/github.com/charmbracelet/x/vt/@v/v0.0.0-20261004011457-ad85c59fdf4e.mod), [immutable emulator source](https://github.com/charmbracelet/x/blob/ad85c59fdf4ebe61d54a4d316d84bf16838beb55/vt/emulator.go), [concurrency wrapper](https://github.com/charmbracelet/x/blob/ad85c59fdf4ebe61d54a4d316d84bf16838beb55/vt/safe_emulator.go), [Bubble Tea 2.0.10](https://pkg.go.dev/charm.land/bubbletea/v2@v2.0.10), [Bubble Tea migration guide](https://github.com/charmbracelet/bubbletea/blob/v2.0.10/UPGRADE_GUIDE_V2.md), [ANSI 0.11.8](https://proxy.golang.org/github.com/charmbracelet/x/ansi/@v/v0.11.8.info).

## Screen, rendering and bounded state

Maintain one emulator for the explicitly attached session and attachment generation. Preserve bytes across WebSocket frame boundaries; a UTF-8 character or ANSI sequence may span frames. Take complete cell-by-cell snapshots, including untouched rows; do not assume the dependency's touched-row Draw path gives a complete fresh surface. Render cells, styles and cursor position from the emulator's current screen, with the cursor clipped to the terminal region. Never splice a raw byte tail into the application view.

The emulator exposes Unicode-aware width machinery. Fixtures must establish actual handling of combining marks, wide characters, emoji and split sequences. The selected version always computes grapheme-cluster cell widths with `ansi.GraphemeWidth` in `flushGrapheme`, including when the child has not enabled Unicode-core mode. `WidthMethod()` reports that mode; it does not select the width actually stored in those cells. Render consistently with the stored grapheme cell widths, and prove cell/render-column agreement in both reported modes. The fixture must observe the rendered output and following cursor/columns, not only inspect the emulator's own cells. Do not treat source APIs as proof of every Unicode case. Main and alternate screens have separate state. Leaving alternate mode restores the main screen; alternate mode must not display main-screen scrollback merely because the dependency's history accessor returns it.

Clamp the usable region to positive dimensions before allocation and before sending resize. Use limits of500 columns, 200 rows and 100,000 cells per screen. Retain at most1,000 main-screen history lines and 200,000 history cells, whichever is smaller at the current width. Do not retain an unlimited per-session transcript or a hidden emulator for every catalog session. The selected dependency configures a 4 MiB parser data size; this is a configured capacity, not a verified eager allocation. Account for its resource cost separately.

The complete immutable module source now establishes an alternate-history exception: both screens start with `DefaultScrollbackSize=10000`; `Scrollback.Push` evicts the oldest line at that bound. The public emulator only exposes main-history adjustment. At the 500-column maximum, hidden alternate history can therefore retain up to 5,000,000 cells, beyond the main-history budget. Console cannot claim the 200,000-cell cap applies to alternate history. Task 29 must measure long alternate-screen scrolling and resize churn, verify eviction and acceptable bounded memory under the selected limits, and record the actual bound. If that dependency allowance cannot satisfy the resource requirement, stop for a revised emulator/API choice; do not silently retain it as qualified or reset an active screen and lose its state. See the [immutable scrollback source](https://github.com/charmbracelet/x/blob/ad85c59fdf4ebe61d54a4d316d84bf16838beb55/vt/scrollback.go).

Panel scroll controls browse the emulator's main-screen history without sending those actions to the child. While terminal input is focused, PageUp/PageDown remain child keys. Clearly distinguish viewing history from following the live cursor. Return to the live bottom before resuming terminal input. Resizing or leaving alternate mode must keep the viewport within valid bounds; preservation/reflow behavior is verified rather than assumed.

## Input and focus

Terminal focus is explicit. Send text, Unicode, Enter, Tab, Escape, navigation, modifiers and paste through the emulator's mode-aware input encoder. Forward Ctrl+C as the interrupt byte while the child has focus; `q` and Escape are ordinary child input. Application shortcuts act only in panel/form focus. Keep the documented Ctrl+] escape as local detach, including during alternate-screen applications. Detach releases only this client's control, closes its attachment and returns focus to the panel; it does not stop the child.

Bracketed paste, application-cursor and focus behavior depend on the child's negotiated modes and the host's actual input events. The selected encoder does not implement Kitty, CSI u or XTerm modifyOtherKeys and removes BaseCode/ShiftedCode. Do not advertise these enhanced protocols or silently claim fidelity for their keys. Verify supported legacy modifiers; unsupported host combinations remain explicitly unsupported until a separate implementation and proof exist. Send focus reports only when supported and only with current input ownership. Do not advertise mouse or keyboard protocol support that fixtures and the host have not established. Child title, working-directory and OSC content are untrusted metadata; they cannot launch programs, change application paths, write the clipboard or escape the terminal region.

User input, encoded paste and terminal replies share a serialized outgoing stream. Preserve byte order. Bound pending input/output to 1 MiB each and individual logical WebSocket messages to 64 KiB. Explicitly set the 64 KiB message read limit on both the TUI terminal connection and the server terminal connection; coder/websocket 1.8.15 defaults to 32 KiB. Split larger encoded input/replay into messages within the limit while preserving byte order. Never disable read limits. Reject oversized paste visibly before sending any of it. A locally failed or uncertain write is not a receipt that the CLI consumed input. Never replay input or parser replies after reconnect, cancellation, transfer or a generation change.

## Owned lifecycle and concurrency

Choose plain `vt.Emulator`, not `SafeEmulator`, inside a Console-owned serialized adapter for screen mutation, resize, key/paste encoding, viewport snapshots and disposal. Async transport commands return generation-tagged messages; Bubble Tea Update/View must not wait on a socket or the emulator's synchronous input pipe.

The dependency uses `io.Pipe` for encoded input and parser replies. Start a continuously serviced reply drain before any operation that can emit them. Draining must not depend on panel rendering, a network write or continued ownership: read-only viewers discard replies, and a full outgoing queue triggers a visible failure while the drain remains able to complete owned shutdown.

`SafeEmulator` alone is insufficient. Its writers can block while holding a lock, Read is not locked, and Close is inherited and writes a flag read by Read/Write. The selected plain emulator exposes `InputPipe()` backed by its `io.PipeWriter`. Qualify that public writer as an `io.Closer`; owned shutdown closes the pipe to unblock reply reads/writes without touching the emulator's closed flag, joins reader and producer completion, and only then calls emulator Close. Keep that pipe closure distinct from final emulator disposal. Task 29 must prove the resulting happens-before relationship. Do not call library Close concurrently with Read or screen mutation, and do not assume a mutex or an unjoined goroutine solves pipe shutdown. Race fixtures must prove cancellation while idle, while emitting a reply, during a write and during resize. If the selected API cannot satisfy these requirements, stop and return the concrete failing proof for a revised technical choice; do not patch vendored dependencies or silence the race detector.

Cancellation detaches only the owned attachment, drains/joins its goroutines and invalidates its generation. Discard queued input on failure. Preserve a frozen last screen for inspection with a clear disconnected/stopped/error state. No late frame, resize, ownership response or outgoing command from an old attachment can affect a newly selected session.

## Session identity and control

Select an actual session ID from authoritative session/member/account/workspace facts. Never attach the first session by proximity or substitute a task row index for a session/member ID. Creating a new session is a separate explicit action with its selected account, mission/member and command; attach cannot silently create one.

Use the actual TUI client ID on both control requests and terminal attachment. Viewing attaches without taking input control. Explicit acquisition calls the typed control operation with `surface=tui`; successful acquisition transfers control under the backend's existing rules. Another client's consent is not part of that current API. Sending `surface=none` relinquishes only the current owner client and clears it; a 403 must not trigger a second release or acquisition.

Input and resize remain guarded by the backend's current owner-client check. Refresh ownership from authoritative state/events and visibly distinguish owned, another owner, unowned and uncertain. A lost or failed mutating control reply cannot authorize local input; inspect the exact target before another explicit action. Never synthesize a recipient client ID or automatically reacquire ownership after reconnect. Currently the socket silently ignores permission-denied input and resize errors; it cannot itself prove loss of ownership. Ownership detection therefore requires authoritative events or bounded polling, with cancellation and generation checks. Terminal input has no server delivery acknowledgment; the interface may show it was sent but cannot claim it was processed.

Stop is an explicit action on the selected actual session ID. It terminates that owned process through the backend and is distinct from Ctrl+C, release, detach, navigation and quitting the TUI. Verify stopping one session leaves another session alive. A missing or already exited session remains a recoverable terminal state.

## Existing wire protocol and necessary transport corrections

The existing authenticated terminal socket sends PTY output as binary frames and accepts input as text frames. Resize is a text JSON message with `type=resize`, integer `cols` and `rows`. The current handler attempts to decode every incoming frame as a control message, so a literal JSON paste can be mistaken for resize. Task 29 sends TUI input as binary frames and makes the handler treat binary data exclusively as child input; text resize and legacy text input remain wire-compatible. The web client's legacy text-paste ambiguity remains: a resize-shaped JSON paste can still be consumed as control. This is an explicit QA41 case requiring its own web proof/fix; TUI binary-input disambiguation does not close that web issue. Prove that pasting a literal resize-shaped JSON object reaches the selected child unchanged and does not resize it. Preserve the bearer/query authentication contract and exact session/client identity. Do not introduce a second PTY engine or change persisted entities, Core execution, approval receipts or provider authentication.

Current Manager.Subscribe retains the last 64 KiB of raw output, replays outside the subscription mutex and permits live output to overtake replay. The API's 32-chunk queue silently drops output when full. Those properties can corrupt an emulated screen even when the frontend parser is correct. Task 29 therefore includes a bounded transport correction: distinguish binary input from resize control, order initial replay before live delivery, prevent obsolete subscriber callbacks from affecting a new attachment, and close an overloaded attachment with WebSocket code 1013 and the exact non-sensitive reason `terminal_output_overflow`, rather than silently dropping bytes. The TUI displays an overload state distinct from normal closure; inspection and explicit reconnect remain available without input replay. Retain the normal close/transport-error distinction, and verify the server read-limit boundary closes oversized messages with code 1009. A terminal size/type control violation may use code 1008 with an explicit stable reason; define and test each introduced reason before use. Add real HTTP/WebSocket/PTY ordering and overload regressions before changing these paths. This widens Task 29 only to terminal subscription/stream handling in `internal/session/manager.go` and `internal/api/server.go`, with the closed test paths `internal/session/manager_test.go`, `internal/session/terminal*_test.go`, `internal/api/server_test.go` and `internal/api/terminal*_test.go`. The handler is shared by the web client: prove unchanged authentication, legacy text input/resize, binary output, control and ordinary close behavior, plus observable overload closure through the web terminal consumer. Run the complete 165-test web suite; synthetic fixtures are not a real-browser/provider qualification.

The 64 KiB replay is a bounded byte history, not a serialized terminal screen. It may start inside UTF-8 or ANSI data and cannot reconstruct every previous screen. On socket loss, expose disconnection; reconnect only through an explicit action, with a fresh emulator, the same selected session and no input/control replay. A reconnect view is provisional until the child redraws. Offer an explicit user-directed repaint/key action where applicable; do not inject Ctrl+L or restart the CLI automatically. Do not claim seamless screen reconstruction from this protocol.

## Signals and outer application lifecycle

With child input focused, Ctrl+C, Ctrl+Z, Ctrl+\ and Ctrl+D forward ETX(0x03), SUB(0x1a), FS(0x1c) and EOT(0x04), respectively. Their effect belongs to the child's terminal mode: canonical ISIG can interrupt/suspend/quit, and EOT can cause EOF in canonical mode; in raw mode these are input bytes. They are never an implicit Console Stop action. Fixtures prove both byte delivery and relevant disposable Unix child signal/EOF behavior; test-owned cleanup may resume/terminate only its own suspended fixture.

An outer TUI SIGWINCH updates the local region and coalesces resize storms to the latest valid dimensions, sending resize only with current ownership. Catchable outer SIGINT, SIGTERM and SIGHUP release this client's control when still owned, close and join the attachment, restore the host terminal and exit without stopping the selected child or daemon. SIGTSTP performs that local cleanup before suspending the TUI; SIGCONT restores panel focus with frozen/retained selection and no automatic control acquisition or input replay. Ctrl+Z in child focus is not outer suspension. SIGKILL and SIGSTOP cannot run cleanup; do not promise joined cleanup for them. Unix suspension/signal behavior is verified on macOS/Linux, with native Windows behavior qualified separately.

## Required proofs

Run `go test ./internal/tui ./internal/client ./internal/api ./internal/session -count=1`, their race regressions, and `sh scripts/qualify-terminal.sh --fixtures`. The script uses disposable local processes and isolated daemon/account homes, never provider credentials or a user's running CLI. Retain observed red→green output and exact IDs. Run the complete release profile and current package checks after source/dependency changes.

| Fixture | Observable requirement |
| --- | --- |
| Cursor movement, overwrite, erase line/screen and scroll regions | Exact cell contents/cursor, no accumulated escape text |
| Main→alternate→main and long alternate scrolling | Correct restoration/main-only visible history; verify 10,000-line hidden-alt eviction, resource bound and resize churn, or stop without qualifying it |
| Split UTF-8/ANSI, combining marks, CJK and emoji | Stable cell boundaries and independently observed rendered columns/cursor in both modes, including ZWJ emoji/flags; no byte-tail corruption |
| Resize, very small/large dimensions and viewport changes | Positive bounded allocation, exact selected-session resize and clipped cursor |
| Text, supported legacy modifiers, application keys and paste | Mode-correct bytes; explicit enhanced-protocol limits and panel shortcuts remain local |
| Child Ctrl+C/Ctrl+Z/Ctrl+\/Ctrl+D, canonical signals/EOF and raw mode | Exact bytes or configured child signal/EOF effects; no implicit Stop or unrelated process mutation |
| Outer SIGWINCH storms, SIGTSTP/SIGCONT, SIGINT/SIGTERM/SIGHUP | Latest valid owned resize; cleanup/join/host restoration with child surviving outer exit/suspend and no automatic reacquisition |
| Focus changes and parser replies | No deadlock, no reply from a read-only or obsolete attachment, no capability claims without evidence |
| Two authenticated clients and two sessions | Explicit transfer, owner-only input/resize/release; no unrelated process stop |
| Literal resize-shaped JSON paste | Exact child input, no unintended control/resize |
| Replay >32 KiB, 64 KiB limits and oversized messages | Complete ordered replay/fragmented input; explicit 64 KiB read limits at both ends and code 1009 beyond limit |
| Replay/live overlap and queue overload in TUI and web consumers | Ordered bytes; code 1013/`terminal_output_overflow` visible instead of loss, with legacy text/resize/auth ordinary-close compatibility |
| Idle/reply/write/resize cancellation and repeated detach | Bounded complete cleanup, zero race reports, unchanged target process |
| Socket loss, reconnect and delayed old messages | Explicit reconnect, fresh/provisional screen, no input/control replay or cross-session updates |
| Process exit/stop and daemon shutdown | Visible ended state and joined transport; no hanging TUI reader |
| Codex and Claude real interaction | Official login and interactive prompts under explicitly selected isolated accounts; later platform-specific qualification, not fixture success |

Windows compilation, a Linux fixture under WSL, installed provider executables and a successful synthetic terminal are separate facts. None establishes native Windows ConPTY, authentication isolation, live provider operation or stable release readiness.
