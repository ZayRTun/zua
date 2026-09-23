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
The transcript rendering of one tool call — its name, arguments, and outcome.
_Avoid_: tool call display, action

**Collapsed Entry**:
The single-line form of a tool card (⏺ name(args) ⎿ outcome) shown when the transcript is not verbose.
_Avoid_: summary line, folded card

**Verbose Transcript**:
The transcript expanded to show every tool card in full; toggled with ctrl+o.
_Avoid_: debug view, full mode

**Command Menu**:
The autocomplete popup above the prompt box, listing slash commands and user-invoked skills matching the typed prefix; enter accepts, it does not send.
_Avoid_: autocomplete dropdown, suggestion list

**Tool Call**:
An invocation the model requested, identified by its call_id; distinct from its card.
_Avoid_: (always distinguish from Tool Card)

### Interface

**Prompt Box**:
The bordered input area at the bottom where the user types prompts and slash commands.
_Avoid_: editor (an implementation detail), input field

**Status Line**:
The line above the prompt box showing working/idle state and token usage.
_Avoid_: footer, status bar

**Session**:
A persisted, resumable conversation stored under `<workspace>/.harness/sessions`.
_Avoid_: chat, conversation history

### Configuration

**Pristine Mode**:
Default tool configuration matching the upstream benchmark: Bash + ViewImage (+ SkillUse).
_Avoid_: minimal mode

**File-Tools Mode**:
Pristine plus the Read/Write/Edit tools, opted into via `-file-tools`.
_Avoid_: full mode
