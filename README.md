# unreal-agent-tui

A terminal UI for the [unreal-agent](https://github.com/unreallabsai/unreal-agent)
harness — a pair-programming agent for your project, in the style of a coding CLI.

Two binaries:

- `unreal-tui-agent` — headless runner: the harness plus added **Read**, **Write**,
  and **Edit** file tools on top of the built-in Bash/ViewImage tools. Accepts a
  JSON request, streams session items as JSONL, and persists resumable sessions
  under `<workspace>/.harness/sessions`.
- `unreal-tui` — the bubbletea terminal UI. Spawns `unreal-tui-agent` per turn
  and renders the transcript, tool calls, and responses.

## Build

Requires Go 1.27+.

```sh
go build -o bin/ ./cmd/...
```

## Run

```sh
export OPENAI_API_KEY=...            # or OPENROUTER_API_KEY / FIREWORKS_AI_API_KEY
zua -workspace ./my-project
zua -workspace ./my-project -provider openrouter -model google/gemini-2.5-pro
```

Keys: `Enter` sends, `Esc` aborts the running turn (or quits when idle), `Ctrl+C` quits.

The headless runner also works standalone:

```sh
zua-agent -workspace ./my-project -p 'Summarize this project.'
```

## Architecture

```
cmd/unreal-tui          bubbletea UI (internal/tui)
cmd/unreal-tui-agent    headless harness runner (internal/runner)
internal/filetools      Read/Write/Edit tool translators + operation manager wrapper
```

The runner wires the public unreal-agent packages directly (coordinator, inbox,
contextbuilder, session store, LLM clients) and wraps the local operation
manager so file operations execute durably like shell operations. Sessions
resume by `session_id`, which the TUI tracks across turns.

Providers: `openai` (default), `commandcode`, `openrouter`, `fireworks`, `ollama` — set via
request field or `UNREAL_TUI_PROVIDER`; model via `UNREAL_TUI_MODEL` or request.

## UI features

- Markdown rendering (glamour) with syntax-highlighted code blocks
- Tool-call cards: Bash commands, Read/Write paths, and colorized `- / +` diffs for Edit
- Session usage in the status line (tokens in/out for the last turn)
- Slash commands: `/new`, `/resume` (session picker), `/model [id]`, `/reload`, `/skills`, `/help`, `/quit`
- Growing multiline editor (Enter sends; Alt/Shift+Enter for newline)
- Tool calls and statuses appear as they happen; see DESIGN.md for the
  streaming notes (token-level streaming is deferred to upstream harness support)

## Cost: pristine vs file tools

The harness's published cost savings assume a minimal tool surface. By default
this project runs the **pristine** configuration (Bash + ViewImage + skills,
upstream system prompt) — matching the benchmarked setup. Opt into the extra
Read/Write/Edit tools when you want diff cards and structured edits:

```sh
zua -workspace ./my-project                # pristine (default)
zua -workspace ./my-project -file-tools    # + Read/Write/Edit
```

The status line shows last-turn and cumulative session token usage, so you can
A/B both modes on your own workload.

## Install as a command

```sh
make install        # go install ./cmd/... → zua + zua-agent in ~/go/bin
```

Then run it inside any project directory (workspace = current directory):

```sh
cd ~/my-project
zua                                   # pristine config, workspace = .
zua -file-tools                       # with Read/Write/Edit + diff cards
zua -provider commandcode             # GLM-5.3-Flash via Command Code
```

`zua-agent` is the headless variant (JSON request in, JSONL events out).

### Skills & the command palette

zua scans `<workspace>/.harness/skills/` plus any extra directories passed via
`-skills ~/.agents/skills` (comma-separated). Skills work pi-style:

- **Model tools**: registered with the harness → the model can call them
  (`SkillUse`). Each adds schema tokens to every request.
- **User-invoked**: frontmatter `disable-model-invocation: true` (pi
  compatible) → never registered, **zero tokens until used**. Invoke with
  `/skill:name [args]`, which injects the skill instructions into the prompt.

Typing `/` opens a command palette above the input: filtered as you type,
↑/↓ to select, Tab to complete, Enter to invoke, Esc to dismiss.
`/skills` lists discoveries, `/reload` re-scans.

### Skills — pi-compatible discovery (no flags needed)

zua scans the same Agent Skills locations as pi, automatically:

1. `<workspace>/.harness/skills/` — harness-native
2. `.agents/skills/` from the workspace up through ancestors (stops at repo root)
3. `~/.agents/skills/` — user skills

Discovery is recursive; first location wins on name collisions; skills without
a description are skipped (as in pi). Non-flagged skills are advertised to the
model in the system prompt and loadable via the `SkillUse` tool. Skills with
`disable-model-invocation: true` never reach the model — invoke them with
`/skill:name [args]` (zero tokens until used). Typing `/` opens a command
palette (type to filter, ↑/↓ select, Tab complete, Enter invoke, Esc dismiss).
`-skills dir1,dir2` adds extra directories.
# zua
