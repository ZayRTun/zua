# unreal-agent-tui design

Decisions locked with the project owner; implement in this order.

## Principles

- Wrap the public unreal-agent harness; never fork it. If a feature needs
  harness internals, defer until upstream supports it.
- Follow pi's interaction semantics; take Claude Code's visual identity and
  transcript conventions (decided 2025-09-23 with the UI redesign).

## Decisions

| Area | Decision |
| --- | --- |
| Tool safety | Auto-run all tools (pi default: no per-call approval). Revisit later; if added, gate at the operation-manager wrapper. |
| Rendering | Assistant text rendered as markdown (glamour) with syntax highlighting; tool calls as collapsible cards; Edit/Write cards show colorized diffs. |
| Streaming | Tool calls and statuses render as they arrive. **Token streaming deferred**: upstream's `IncludePartialMessages` is accepted but ignored — the adapter only returns complete responses and its SSE parsing is internal. Re-evaluate when the harness emits partials. |
| Sessions | Slash commands: `/new`, `/resume`, `/model [id]`, `/reload`, `/skills`, `/quit`, `/help`. Session picker lists `<workspace>/.harness/sessions/*.session.jsonl` by mtime with first user input as title. |
| Provider | Selected at startup via flag/env; `/model` switches model id for subsequent turns. |

## UI layout (alt-screen)

```
┌ header ────────────────────────────────────────────┐
│ unreal-agent · workspace · model · session 1a2b3c4 │
├ transcript (viewport, scrolls, auto-follow) ───────┤
│ ❯ user prompt (bold)                               │
│ · reasoning summary (dim italic)                   │
│ ⚙ Bash (bash)                                      │
│ ┌ diff card: Edit main.go ─────────────────────┐   │
│ │ - old line        + new line                 │   │
│ └──────────────────────────────────────────────┘   │
│ assistant markdown (glamour)                       │
├ status ────────────────────────────────────────────┤
│ working… · last turn: 1.2k in / 0.3k out           │
├ editor (grows 1–8 lines with content) ─────────────┤
│ ❯ _                                                │
└────────────────────────────────────────────────────┘
```

Keys: `Enter` send · `Esc` abort turn / quit when idle · `Ctrl+C` quit ·
`/`-commands in the editor.

## Streaming contract (runner → TUI JSONL)

First line: `{"type":"meta","session_id",...,"model","workspace"}` then
session-store items (`Kind`: input/turn/model_response/tool_call_status/fork)
and `{"type":"error"}`. The TUI decodes `model_response` output items
(message/tool_call/reasoning) and keeps a call_id → (name, arguments) map to
render tool cards when the matching `tool_call_status` arrives.

## Tool cards

- Decode arguments from the recorded `tool_call` item at card time (no I/O).
- `Edit`: real line-level diff (LCS in `hunkDiff`) rendered as hunks — changed
  lines with up to 2 lines of surrounding context, runs of unchanged lines
  collapsed to a `⋯` gap marker, capped at 24 lines. Edits larger than 400
  lines per side skip the DP table and fall back to all removals then all
  additions (`formatChanges`). Identical old/new renders a `(no changes)`
  placeholder instead of an empty card. Covered by `internal/tui/render_test.go`.
- `Write`: "create/replace path" + first lines of content, collapsed.
- `Read`: path (+offset/limit) only.
- `Bash`: command string truncated to one line.
- Non-terminal statuses render as pending; terminal update replaces the card
  line (matching by call_id in the transcript buffer).

## Session picker

Scan session files (mtime desc), title = first external input payload
(decoded JSON string, collapsed to 60 runes). `↑/↓` select, `Enter` load,
`Esc` cancel. Loading a session sets `session_id` and replays user inputs +
assistant messages from the file into the transcript (tool cards skipped).

## UI redesign (2025-09-23, in planning)

Locked scope: visual reskin + interaction feel (collapsible transcript
entries, transcript-expand toggle, redesigned prompt box) in alt-screen.
No token streaming (upstream constraint unchanged). Frozen: runner JSONL
contract, session format, CLI flags. Keybindings may evolve.

## Out of scope (for now)

- Approval prompts (see Decisions), themes, mouse support, image tools in the
  UI. (User-invoked and workspace skills are now surfaced via `/skills` and
  `/reload`; deep extension support remains out of scope.)

## Transcript behavior (added with the v2 rendering pass)

- **Auto-follow with stickiness.** `refresh()` snaps to the bottom only when
  the viewport is already at the bottom; if the user scrolled up to read,
  their position is preserved and following re-engages when they page back
  down. Scroll keys (`pgup/pgdown/home/end/up/down/ctrl+u/ctrl+d` — the
  viewport's vi-style bindings excluded so typing never jumps the
  transcript) are forwarded to the viewport; everything else stays with the
  editor.
- **Per-block render cache.** Each block caches its rendered output keyed by
  width (`block.rendered`/`block.width`), so an event re-renders only the
  changed block, not the whole transcript. Tool-card status changes
  invalidate the card's cache entry (`updateToolCard`).
- **Agent stderr is captured, not discarded.** A thread-safe `stderrTail`
  writer keeps the last 40 lines of the agent process's stderr; on a nonzero
  exit the tail is appended to the "agent exited" error block, so failures
  like a missing API key show their actual cause. Hidden overflow is noted
  ("… N earlier lines hidden").
- **Input hygiene.** OSC 10/11 color-query replies that race with input
  reading are detected (`isOSCGarbage`) and dropped before they can reach
  the editor.
- **Live config surfacing.** `/reload` re-discovers workspace skills and
  prints the tools/skills/model the next turn will run with; `/skills`
  lists discovered skills and whether each is a model tool or user-invoked.
  The runner is spawned fresh per turn, so discoveries apply automatically —
  the commands only surface them.

## Cost alignment (added after reviewing unreallabs.ai/blog/unreal-agent)

The published cost savings come from (a) minimal harness footprint — few tool
schemas, simple prompts, no sub-agents — and (b) more tool work per turn via
the async tool model. Their benchmark tables correlate more tool definitions
with more tool calls and input tokens (Pi: 27–60 tools → 57–75 calls;
unreal-agent: ~5 tools → 27–38).

Consequences for this project:

- **Pristine by default.** The runner now mirrors upstream exactly: Bash +
  ViewImage (+ SkillUse when skills exist), upstream default system prompt,
  $SHELL resolution, operation directory under the session store. No file
  tools, no extra prompt. This is the configuration their cost numbers were
  measured against.
- **File tools are opt-in** via `-file-tools` (runner flag or TUI flag, or
  `"file_tools": true` in the JSON request). They add schema tokens but may
  save turns on edit-heavy work; treat as an empirical, per-workload choice.
- **Usage in the status line**: the TUI accumulates per-turn and session
  input/output tokens from model_response usage, enabling direct A/B cost
  comparison between pristine and file-tools runs.
