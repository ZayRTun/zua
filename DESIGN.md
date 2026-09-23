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
assistant messages + tool cards from the file into the transcript (issue #2:
cards are rebuilt from recorded tool calls and their final
`tool_call_status`, so a resumed transcript renders exactly like a live one
through the same collapsed/verbose path).

## UI redesign (2025-09-23, locked with project owner)

Scope: visual reskin + interaction feel (option b). Full CC clone features
(`!` bash mode, `@` file mentions, `#` shortcuts) stay out of scope.

**Rendering**: stays alt-screen (deliberate rejection of CC's inline model —
keep viewport-managed scrolling, mouse support). Composer is the redesigned
surface, not the render model.

**Composer**: thin dim rule, accent `❯` prompt, editor, thin dim rule — no
border, no placeholder, no hint line (the hint chrome was replaced by the
Usage Line). Keys unchanged: Enter sends, shift/alt+enter newline (works both
as raw esc+return and as disambiguated CSI-u / modifyOtherKeys
sequences, which bubbletea v1 drops — surfaced via a tea.WithFilter
CSI translator).

**Command Menu**: popup above the box when input starts with `/`; static
commands plus dynamic `/skill:name` entries (user-invoked skills from
frontmatter). Case-insensitive prefix filter. ↑/↓ select (not scroll), tab
completes, esc dismisses (before quit), **enter accepts into the input —
does not send**; a second enter sends. CC-identical.

**Chrome palette**: header, Tool Cards, and Usage Line use a dim-gray
+ single-accent palette (accent 12); assistant markdown keeps glamour
auto-style. Usage Line shows the current model id next to the usage
segments — the tool configuration has no mode concept (amended 2026-07: modes
retired). While a Turn runs the spinner
shows a randomized gerund verb (chosen per turn, rendered dim). All
chrome degrades to plain text under NO_COLOR / no-color terminals.

**Transcript**: tool cards collapse to one-line Collapsed Entries by default
(⏺ Bash($ cmd) ⎿ ok · Read: path+lines · Write: path+bytes · Edit: path
+added/-removed, computed at render time from decoded args). Known limit:
Read's collapsed line count appears only when the call recorded a `limit`
arg — full-file Reads show the path only, since counts come from decoded
args alone (no file I/O in the UI). ctrl+o toggles
Verbose Transcript in place — dual rendering per block, keyed with the
per-block cache; fresh cards render collapsed while collapsed. User prompts
and assistant messages never collapse.

**Chrome**: glamour auto-style kept for markdown; header/cards/Usage Line
restyle to dim gray + accent 12. Respect NO_COLOR. Spinner adopts randomized
gerund verbs. Usage Line ends with the raw model id. Mouse cell-motion kept.

**Frozen**: runner JSONL contract, session file format. Token streaming
remains deferred (upstream constraint). Amended 2026-07 (issue #9): the
`-file-tools` flag and the `file_tools` request field are removed — Read/
Write/Edit are always enabled and the CLI-flag freeze is lifted for that
surface.

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

Consequences for this project (amended 2026-07, issue #9 — the flat OpenCode
Go subscription makes per-token cost optimization moot, so the modes die):

- ~~**Pristine by default.**~~ Retired. The runner always runs Bash +
  ViewImage (+ SkillUse when skills exist) **plus the Read/Write/Edit file
  tools**, pair-programming system prompt, $SHELL resolution, operation
  directory under the session store.
- ~~**File tools are opt-in** via `-file-tools`...~~ Retired. There is no
  mode flag, no `file_tools` request field, no mode label anywhere.
- **Usage in the Usage Line**: the TUI accumulates per-turn and session
  input/output/cache token counts from model_response usage.

## Settings File (added with the settings ticket, issue #10)

- **One global `~/.zua/settings.json`** holds `provider`, `api_key`,
  `base_url`, `model`, and `thinking_level`; one shared loader serves both
  binaries. Every field resolves with the same precedence: CLI flag /
  request value > `OPENCODE_*` env var > settings file > built-in default.
- **Defaults are provider-aware**: provider `opencode-go`, base URL
  `https://opencode.ai/zen/go/v1`, model `glm-5.3-flash`, thinking level
  `high`. When another provider is resolved, its model/base URL fall back to
  the client's own defaults.
- **The key never travels in argv**: the TUI passes the resolved
  configuration in the JSON request on the agent's stdin; the error for a
  missing key names the settings file; errors never contain file content
  (tested). The settings path is gitignored.
- The TUI forwards `/model` overrides with the next request; resolved
  values win at the request tier of the runner's own precedence chain.

## OpenCode Go provider (added with the provider ticket, issue #11)

- **Chat-completions adapter, in-repo** (`internal/opencodego`): implements the
  harness's public one-method `llm.Adapter` by wrapping harness primitives
  (`RemoteClient` for transport + retry policy) — never forks the harness. The
  gateway's `/responses` endpoint returns 503 for GLM models, so the adapter
  speaks `/chat/completions`.
- **Mandatory session header**: every request carries
  `x-opencode-session: <zua session id>`; the runner attaches it after the
  session opens (via a `sessionSetter` interface assertion). A request without
  it gets HTTP 400 `MissingSessionID` (gateway behavior, mirrored by the fake
  server).
- **Item translation**: assistant turns merge harness reasoning + tool-call
  items into one chat message (`reasoning_content` + `tool_calls`); tool
  results replay as `role: "tool"` messages; GLM reply `reasoning_content`
  becomes a reasoning item. Raw reasoning state replays verbatim.
- **Usage**: `prompt_tokens` → input, `prompt_tokens_details.cached_tokens` →
  cache-read, `completion_tokens` → output (+ nested `reasoning_tokens`);
  cache-write is not reported by chat-completions (stays 0).
- **Thinking levels**: `low`/`high`/`max` pass through as `reasoning_effort`;
  `medium` clamps to `high`, `xhigh` clamps to `max`, empty defaults `high`.

## Chrome reskin (issue #12, T5)

- **Launcher Header**: the resume screen (the launcher) gains a Header — an
  original zua pixel-mascot (`logoArt`, accent-12 block critter with eye/leg
  gaps left to the terminal background), the bold app name, the workspace
  path, and a live stats line `model · thinking level · N skills` exactly in
  that format (per spec; the count derives from `m.skills` on every render).
  Every header and picker line clips to the terminal width.
- **Transcript view loses its header and the full-width divider** — the
  conversation gets the reclaimed viewport height. Only the Composer's own
  rules remain as full-width lines.
- **Composer**: thin rule above, accent `❯ ` prompt (accentColor 12), input,
  thin rule below (dimColor 8). No rounded border, no placeholder text, no
  hint line — the Usage Line (issue #13) replaces the hints. Editor width is
  terminal width minus the two `❯ ` prompt columns; multi-line input grows
  the editor between the rules (prompt glyph only on the first line).
- **The old status line temporarily survives** below the transcript/top of
  the launcher until issue #13 replaces it with the Usage Line.
- **Launcher interaction is unchanged from pi's semantics**: the picker
  consumes keys while open (select/Esc); slash commands like /reload are
  typed in the transcript view, and the header stats re-derive live on the
  next launcher render — that is what "skills count refreshes on /reload"
  means here.

## Usage Line replaces the status line (issue #13, T6)

- **The old status line and its tests are deleted** (the hint line went with
  issue #12). The bottom chrome is now, top to bottom: Command Menu (when
  open), Composer, and the single Usage Line — the glossary's "below the
  Composer" position.
- **Usage Line format**:
  `↑in ↓out R… W… CH…% $… pct%/window - model • thinking level`.
  `↑ ↓ R W` are session-spanning totals (accumulate over every turn's
  usage); `CH` is the latest turn only — cacheRead ÷ prompt tokens, hidden
  when no cache was reported (as are R/W). Context percentage colorizes
  past 70% (warn) / 90% (error); the Ascii profile degrades the colors.
- **`internal/catalog`** is the tiny in-repo model catalog: context window,
  max tokens, per-1M rates, thinking levels, transcribed from pi-ai's
  generated OpenCode Go catalog for `glm-5.3-flash`. `Model.Cost` folds
  buckets × rates; the harness's InputTokens is inclusive (it already
  contains cache-read/cache-write), so those are carved out before the
  plain input rate. Catalog-missing models omit the `$` and context
  segments — never invented.
- **Wire format**: the cache-read/cache-write buckets already traveled in
  the runner JSONL (the observer marshals the full harness response); the
  ticket's "extension" is that the Usage Line now consumes them, pinned by
  the e2e assertion that `CacheWriteInputTokens` stays present even at 0.
- **`(auto)`** is gated on `autoCompactionVerified` (usage.go), currently
  false: the harness represents compaction turns (session.TurnCompaction)
  but nothing schedules them automatically, so the segment never renders.
  Flip the gate only after verifying real auto-compaction.
- **Spinner + gerund verb** prefix the Usage Line while a Turn runs (and
  while sessions load); idle shows the plain line with no state label.
- **Token formatting** (`formatTokens`): raw below 1k, one-decimal k below
  10k, rounded k below 1M, then one-decimal M. Costs render with three
  decimals (`formatCost`).

- **Amendment (issue #13 review)**: the event parser now threads the JSONL's
  cache buckets into `usageMsg` (pinned by `TestEventParserUsageCarriesCacheBuckets`)
  — without it R/W, CH, and the cost carve-out were test-only and cached
  tokens were costed at the full input rate. Cost renders whenever the
  catalog resolves and the session has usage (even $0.000), not only when
  cost > 0; zero cache buckets stay hidden (pi-style noise avoidance —
  judgement call on the literal format). `catalog.Cost` takes a named
  `catalog.Usage` struct so the cache buckets cannot be swapped silently.

## Launcher opens at startup (follow-up from real use)

The reskin spec said the launcher shows the Header but never pinned when
the launcher opens; pre-reskin code only loaded sessions on `/resume`, so
plain `zua` booted straight into the transcript — where the Header is
deliberately absent. `Init()` now issues the session load on startup: the
launcher is the first screen (reference-launcher behavior), the Header
above the picker; Esc dismisses to the transcript as before. Pinned by
`TestStartupOpensLauncher` through the real Update → View seam.
