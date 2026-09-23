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
zua -workspace ./my-project
zua -workspace ./my-project -provider openrouter -model google/gemini-2.5-pro
```

The launcher shows the zua Header — pixel-mascot logo, workspace path, and a
live stats line (`glm-5.3-flash · high · N skills`) — with the session picker
below it. In the transcript view the conversation takes the whole screen: no
header, no divider. The Composer is a thin rule, an accent `❯` prompt, the
input, and a rule below — no border box, no placeholder, no hint line
(`/help` in the Composer lists commands; ctrl+o toggles the Verbose
Transcript).

## Configuration

One global settings file, `~/.zua/settings.json` (gitignored — never commit
your API key):

```json
{
  "provider": "opencode-go",
  "api_key": "...",
  "base_url": "https://opencode.ai/zen/go/v1",
  "model": "glm-5.3-flash",
  "thinking_level": "high"
}
```

Every field resolves with the same precedence: CLI flag > `OPENCODE_*`
environment variable > settings file > built-in default. Env vars:
`OPENCODE_API_KEY` (the OpenCode Go key; provider-specific keys like
`OPENAI_API_KEY`/`OPENROUTER_API_KEY` apply to their own provider),
`OPENCODE_PROVIDER`, `OPENCODE_MODEL`, `OPENCODE_THINKING`, and
`OPENCODE_BASE_URL` (the latter opencode-go only). A missing file is fine — defaults apply; malformed JSON fails loudly
naming the file. The key is never printed in the UI or logs.

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

Providers: `opencode-go` (default), `openai`, `commandcode`, `openrouter`, `fireworks`, `ollama` — set via
request field, CLI flag, `OPENCODE_PROVIDER`, or the settings file. The default
provider runs through OpenCode Go's chat-completions gateway
(`https://opencode.ai/zen/go/v1`): flat-rate subscription, GLM models, and a
mandatory `x-opencode-session` header on every request.

## UI features

- Markdown rendering (glamour) with syntax-highlighted code blocks
- Tool-call cards: Bash commands, Read/Write paths, and colorized `- / +` diffs for Edit
- A Usage Line below the Composer: session-spanning token totals
  (`↑in ↓out R… W…`), latest-turn cache hit, cost and context percentage
  from the in-repo model catalog, and `model • thinking level`
- Slash commands: `/new`, `/resume` (session picker), `/model [id]`, `/reload`, `/skills`, `/help`, `/quit`
- Growing multiline editor (Enter sends; Alt/Shift+Enter for newline)
- Tool calls and statuses appear as they happen; see DESIGN.md for the
  streaming notes (token-level streaming is deferred to upstream harness support)

## Tool configuration

There is exactly one tool configuration: Bash, ViewImage, and skills plus the
always-on **Read**, **Write**, and **Edit** file tools (pair-programming
system prompt). Read/Write/Edit add schema tokens but let the model make
structured edits and produce diff cards directly. No mode flag, no mode
label — glossary terms Pristine/File-Tools Mode are retired (see DESIGN.md).

The Usage Line's cost and context segments come from `internal/catalog`
(currently `glm-5.3-flash`: $0.15/$0.50/$0.03 per 1M, 1.0M window) so you can
watch spend and headroom on your own workload. Catalog-missing models omit
those segments rather than invent values.

## Install as a command

```sh
make install        # go install ./cmd/... → zua + zua-agent in ~/go/bin
```

Then run it inside any project directory (workspace = current directory):

```sh
cd ~/my-project
zua                                   # workspace = .
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
