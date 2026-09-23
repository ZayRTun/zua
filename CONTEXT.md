# unreal-agent-tui

A terminal UI for the unreal-agent harness: the user types a request, watches
the agent read, edit, and run things in their project, and converses across
resumable sessions.

## Language

### Conversation

**Turn**:
One user prompt plus everything the agent does until it finishes responding and stops.
_Avoid_: request, round, iteration

**Transcript**:
The scrollable rendering of the whole conversation, oldest at top.
_Avoid_: history, log, chat

**Block**:
One entry in the transcript: user prompt, assistant message, reasoning summary, tool card, error, or divider.
_Avoid_: item, entry, message (reserved for what the model said)

**Tool Card**:
The transcript rendering of one tool call — a `●` status dot, the tool name, its arguments, and its outcome lines hanging off `└` branches.
_Avoid_: tool call display, action, collapsed entry

**Verbose Transcript**:
The transcript expanded to show every tool card's full output; toggled with ctrl+o.
_Avoid_: debug view, full mode

**Tool Call**:
An invocation the model requested, identified by its call_id; distinct from its card.
_Avoid_: (always distinguish from Tool Card)

### Interface

**Header**:
The launcher's top block: logo, app name, workspace path, and a stats line (model · thinking level · skills count).
_Avoid_: title bar, banner

**Composer**:
The input area at the bottom where the user types prompts and slash commands — a `❯` prompt between a rule above and a rule below, no border, no placeholder.
_Avoid_: prompt box, editor (an implementation detail), input field, bordered box

**Usage Line**:
The single line below the Composer: `↑in ↓out R… W… CH…% $… context%/window (auto) - model • thinking level`, computed from the model catalog.
_Avoid_: status line, footer, hint line

**Command Menu**:
The autocomplete popup above the Composer, listing slash commands and user-invoked skills matching the typed prefix; enter accepts, it does not send.
_Avoid_: autocomplete dropdown, suggestion list

**Session**:
A persisted, resumable conversation stored under `<workspace>/.harness/sessions`.
_Avoid_: chat, conversation history

### Configuration

**OpenCode Go**:
The model gateway this app runs against by default — OpenAI chat-completions compatible, requires a per-session `x-opencode-session` header.
_Avoid_: opencode zen, go plan

**Settings File**:
The global `~/.zua/settings.json` holding provider, API key, base URL, model, and thinking level; flags and env vars override it.
_Avoid_: config file, preferences
