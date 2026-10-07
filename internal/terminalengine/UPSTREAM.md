# Terminal engine origin

This package contains the headless parser and screen from Portalis v0.1.0,
commit `4d6e28d0f8f02fe1947f5e3efc6429138ffb5818`. The original MIT license and copyright
notice are retained in LICENSE. UPSTREAM.json records original file hashes.
The published module remains a test oracle, not the runtime emulator.

Only ansi.go, screen.go and terminal_size.go were copied as production
source. Two selection resource constants were extracted from clipboard.go;
no clipboard operations, PTY, controller or Bubble Tea lifecycle were copied.
Lip Gloss v1 is a runtime dependency for its Color type; Bubble Tea/controller
packages belong only to the test oracle and are not invoked. Runtime dependency
initialization remains a required Task29 audit.

Original parser/screen tests are retained without assertion changes. Package
names were changed mechanically to terminalengine.

The Console patch adds Parser.ResizeChecked. Screen reports the actual
main-grid trim; the parser applies that trim and size clamp to its saved DEC
1049 cursor. Call Parser.ResizeChecked for every resize of an attached parser.
Calling Screen.ResizeChecked directly does not update parser-owned state.
Screen also rebases its DECSC/1048 saved cursor using the crop offset of the
buffer where it was saved. ExitAltScreen records when the saved alternate buffer was discarded, so a
subsequent alternate entry cannot associate that cursor with a new grid.
If that alternate buffer was discarded, only size
clamping remains; no discarded text is reconstructed. SaveCursor records the
buffer identity, which reset/soft-reset clear. Non-resize feed, charset, mode
and rendered output is unchanged. The resize patch
is local and must not be described as an upstream v0.1.0 fix. One-row scrolling
remains defective and unsupported by Console. No local Go module replacement
is used. Updates must recheck origin, license, patch and unchanged regressions.

The local render formatting patch caches at most 64 complete foreground,
background and style combinations while rendering one cell row. It reuses
the original renderStyle result without changing its bytes. Each cache is
discarded when that row returns; parser, cells, cursor, selection and held
screen state are not cached or changed. This reduces repeated RGB formatting
in large styled screens. Exact rendered bytes are checked against the
unchanged published module, including more than 64 styles, cursor and
selection overlays, hidden cells and synchronized output.

Before distributing binaries that include this package, include the complete
MIT license and copyright in THIRD_PARTY_NOTICES in every archive. This is a
required Task29 packaging proof, not a completed release claim.

UPSTREAM.patch uses zero-context unified diffs against the exact recorded source
(apply with --unidiff-zero); this preserves Go tabs without context-line
whitespace being mistaken for source indentation by repository diff checks.
