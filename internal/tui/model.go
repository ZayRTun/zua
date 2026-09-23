package tui

import (
	"math/rand/v2"
	"os"
	"path/filepath"

	"fmt"
	"strings"

	"unreal-agent-tui/internal/skills"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// blockKind is one transcript entry.
type blockKind int

const (
	blockMeta blockKind = iota
	blockUser
	blockAssistant
	blockReasoning
	blockTool
	blockError
	blockDivider
)

// toolCard is the state of one tool call shown as a card.
type toolCard struct {
	callID   string
	name     string
	argsRaw  string
	status   string // running, ok, failed, canceled
	errText  string
	outText  string // captured tool output (completed operations)
	exitCode int    // non-zero exit code, 0 = none/unknown
}

// block is one transcript block; tool blocks carry a card.
type block struct {
	kind     blockKind
	text     string
	tool     *toolCard
	rendered string // render cache (assistant markdown, tool cards)
	width    int    // width the cache was rendered at
	verbose  bool   // verbosity the cache was rendered at
}

type Model struct {
	workspace string
	provider  string
	model     string
	fileTools bool
	turnVerb  string // spinner verb chosen for the running Turn

	viewport   viewport.Model
	textarea   textarea.Model
	spinner    spinner.Model
	blocks     []block
	toolBlocks map[string]int // callID → blocks index
	skillDirs  []string       // extra -skills directories
	skills     []skills.Entry // discovered skills (refreshed by /reload)
	menuIndex  int            // Command Menu selection
	menuHidden bool           // Esc-dismissed until the input changes
	sessionID  string
	turnUsage  [2]int64 // last turn: in, out tokens
	sessionIn  int64
	sessionOut int64

	running bool
	aborted bool
	verbose bool // Verbose Transcript (ctrl+O); false = Collapsed Entries
	width   int
	height  int

	cmdCancel func()

	// session picker state
	picking   bool
	sessions  []sessionEntry
	pickIndex int
	loading   bool

	events chan tea.Msg
}

func New(workspace, provider, model string, fileTools bool, skillDirs []string) Model {
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		absolute = workspace
	}
	ta := textarea.New()
	ta.Placeholder = "Describe a task… (/help for commands, Esc to abort/quit, Ctrl+C to quit)"
	ta.Prompt = "> "
	ta.CharLimit = 32_000
	// 78 = 80-wide terminal minus the two border columns of the Prompt Box.
	ta.SetWidth(78)
	ta.SetHeight(1)
	ta.Focus()
	ta.ShowLineNumbers = false

	m := Model{
		workspace:  absolute,
		provider:   provider,
		model:      model,
		fileTools:  fileTools,
		skillDirs:  skillDirs,
		skills:     discoverSkills(absolute, skillDirs),
		textarea:   ta,
		viewport:   viewport.New(80, 20),
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		toolBlocks: map[string]int{},
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// ---- messages ----

type (
	turnDoneMsg   struct{ err error }
	blockMsg      struct{ b block }
	toolStatusMsg struct {
		callID   string
		name     string
		argsRaw  string
		status   string
		errText  string
		outText  string
		exitCode int
	}
	metaMsg struct {
		sessionID string
		model     string
	}
	sessionsMsg struct {
		entries []sessionEntry
		err     error
	}
	usageMsg struct {
		in  int64
		out int64
	}
)

// ---- update ----

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.textarea.SetWidth(max(msg.Width-2, 1)) // -2: Prompt Box border columns
		m.setViewportHeight()
		m.refresh()
	case spinner.TickMsg:
		if m.running || m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	case metaMsg:
		if m.sessionID == "" {
			m.sessionID = msg.sessionID
		}
		if m.model == "" {
			m.model = msg.model
		}
		m.appendBlock(block{kind: blockMeta, text: "session " + short(msg.sessionID) + " · " + msg.model + " · " + m.workspace})
	case usageMsg:
		m.turnUsage = [2]int64{msg.in, msg.out}
		m.sessionIn += msg.in
		m.sessionOut += msg.out
		m.refresh()
	case blockMsg:
		m.appendBlock(msg.b)
	case toolStatusMsg:
		m.updateToolCard(msg)
		// Tool output is live information; re-pin only if the user is reading
		// the tail — otherwise keep their scroll position.
		if m.viewport.AtBottom() {
			m.viewport.GotoBottom()
		}
	case sessionsMsg:
		m.loading = false
		if msg.err != nil {
			m.appendBlock(block{kind: blockError, text: "list sessions: " + msg.err.Error()})
		} else {
			m.picking = true
			m.sessions = msg.entries
			m.pickIndex = 0
		}
		m.refresh()
	case turnDoneMsg:
		m.running = false
		m.cmdCancel = nil
		if m.aborted {
			m.appendBlock(block{kind: blockDivider, text: "turn aborted"})
			m.aborted = false
		} else if msg.err != nil {
			m.appendBlock(block{kind: blockError, text: "agent exited: " + msg.err.Error()})
		}
		m.refresh()
	case tea.KeyMsg:
		if m.picking {
			return m.updatePicker(msg)
		}
		if handled, extra := m.handleMenuKeys(msg); handled {
			cmds = append(cmds, extra...)
			m.setViewportHeight() // menu visibility changed the bottom section
			break                 // fall through to the common exit path (re-arms listener)
		}
		if msg.Type != tea.KeyUp && msg.Type != tea.KeyDown && msg.Type != tea.KeyTab {
			// typing/backspace changed the input → reset menu selection
			m.menuIndex = 0
			m.menuHidden = false
		}
		switch msg.Type {
		case tea.KeyCtrlO:
			m.verbose = !m.verbose
			for index := range m.blocks {
				m.blocks[index].rendered = ""
			}
			m.refresh()
		case tea.KeyCtrlC:
			if m.running && m.cmdCancel != nil {
				m.cmdCancel()
			}
			return m, tea.Quit
		case tea.KeyEsc:
			if m.running && m.cmdCancel != nil {
				m.aborted = true
				m.cmdCancel()
			} else {
				return m, tea.Quit
			}
		case tea.KeyEnter:
			if strings.Contains(msg.String(), "alt+enter") || strings.Contains(msg.String(), "shift+enter") {
				// The textarea's InsertNewline binding only matches bare
				// "enter", so a modified enter arrives here unmatched and
				// would be swallowed — insert the newline at the cursor
				// explicitly.
				m.textarea.InsertString("\n")
				m.resizeEditor()
			} else if !m.running {
				input := strings.TrimSpace(m.textarea.Value())
				if input != "" {
					m.textarea.Reset()
					m.textarea.SetHeight(1)
					if strings.HasPrefix(input, "/") {
						cmds = append(cmds, m.command(input)...)
					} else {
						cmds = append(cmds, m.beginTurn(input, input)...)
					}
				}
			}
		default:
			m.textarea, _ = m.textarea.Update(msg)
			m.resizeEditor()
		}
	}
	// While a turn is active, exactly one listen() must be pending at any
	// time; whatever message was just handled consumed it, so re-arm here on
	// every path. When the turn ends (turnDoneMsg sets running=false) the
	// chain stops naturally.
	if m.running && m.events != nil {
		cmds = append(cmds, m.listen())
	}
	// Scroll keys reach the transcript, but never while the Command Menu is
	// open — ↑/↓ belong to the menu selection then (locked precedence).
	if isScrollKey(message) && !m.menuVisible() {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(message)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// isScrollKey reports whether a message is a transcript-scrolling key.
// The viewport's default keymap also binds vi-style u/d/f/b/space to paging —
// forwarding those while the user is typing made the transcript jump.
func isScrollKey(message tea.Msg) bool {
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch key.String() {
	case "pgup", "pgdown", "home", "end", "up", "down", "ctrl+u", "ctrl+d":
		return true
	}
	return false
}

func (m *Model) resizeEditor() {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	m.textarea.SetHeight(min(max(lines, 1), 8))
	m.setViewportHeight()
}

func (m *Model) appendBlock(b block) {
	if b.kind == blockTool && b.tool != nil {
		if _, exists := m.toolBlocks[b.tool.callID]; exists {
			return
		}
		m.toolBlocks[b.tool.callID] = len(m.blocks)
	}
	m.blocks = append(m.blocks, b)
	m.refresh()
}

func (m *Model) updateToolCard(msg toolStatusMsg) {
	index, exists := m.toolBlocks[msg.callID]
	if !exists {
		card := &toolCard{callID: msg.callID, name: msg.name, argsRaw: msg.argsRaw, status: msg.status, errText: msg.errText, outText: msg.outText, exitCode: msg.exitCode}
		m.appendBlock(block{kind: blockTool, tool: card})
		return
	}
	card := m.blocks[index].tool
	card.status = msg.status
	if msg.errText != "" {
		card.errText = msg.errText
	}
	if msg.outText != "" {
		card.outText = msg.outText
		if msg.exitCode != 0 {
			card.exitCode = msg.exitCode
		}
	}
	if msg.name != "" && card.name == "" {
		card.name = msg.name
	}
	if msg.argsRaw != "" && card.argsRaw == "" {
		card.argsRaw = msg.argsRaw
	}
	m.blocks[index].rendered = "" // invalidate render cache
	m.refresh()
}

func (m *Model) command(input string) []tea.Cmd {
	fields := strings.Fields(input)
	switch fields[0] {
	case "/quit", "/exit":
		if m.cmdCancel != nil {
			m.cmdCancel()
		}
		return []tea.Cmd{tea.Quit}
	case "/new":
		m.sessionID = ""
		m.appendBlock(block{kind: blockDivider, text: "new session — next prompt starts fresh"})
	case "/skills":
		m.skills = discoverSkills(m.workspace, m.skillDirs)
		if len(m.skills) == 0 {
			m.appendBlock(block{kind: blockMeta, text: "no skills found"})
		} else {
			var names []string
			for _, entry := range m.skills {
				tag := "model tool"
				if entry.UserOnly {
					tag = "user-invoked"
				}
				names = append(names, entry.Name+" ("+tag+")")
			}
			m.appendBlock(block{kind: blockMeta, text: "skills:\n  " + strings.Join(names, "\n  ")})
		}
	case "/model":
		if len(fields) == 1 {
			m.appendBlock(block{kind: blockMeta, text: "model: " + orDefault(m.model, "(provider default)")})
		} else {
			m.model = fields[1]
			m.appendBlock(block{kind: blockMeta, text: "model set to " + m.model})
		}
	case "/reload":
		m.skills = discoverSkills(m.workspace, m.skillDirs)
		m.appendBlock(block{kind: blockMeta, text: m.reloadReport()})
	case "/resume":
		m.loading = true
		m.refresh()
		return []tea.Cmd{listSessions(m.workspace), m.spinner.Tick}
	case "/help":
		var lines []string
		lines = append(lines, "commands:")
		for _, cmd := range commandTable { // same table the Command Menu lists
			lines = append(lines, fmt.Sprintf("  %-18s %s", cmd.display(), cmd.desc))
		}
		lines = append(lines, "keys: Enter send · Esc abort/quit · Ctrl+C quit · ctrl+O verbose transcript")
		m.appendBlock(block{kind: blockMeta, text: strings.Join(lines, "\n")})
	default:
		if strings.HasPrefix(fields[0], "/skill:") {
			return m.invokeSkillCommand(input, fields[0])
		}
		m.appendBlock(block{kind: blockError, text: "unknown command " + fields[0] + " (/help)"})
	}
	m.refresh()
	return nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// ---- picker ----

func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.picking = false
		m.refresh()
		return m, nil
	case tea.KeyUp:
		if m.pickIndex > 0 {
			m.pickIndex--
		}
	case tea.KeyDown:
		if m.pickIndex < len(m.sessions)-1 {
			m.pickIndex++
		}
	case tea.KeyEnter:
		if m.pickIndex >= 0 && m.pickIndex < len(m.sessions) {
			entry := m.sessions[m.pickIndex]
			m.picking = false
			m.loadSession(entry)
			return m, nil
		}
	}
	m.refresh()
	return m, nil
}

func (m *Model) loadSession(entry sessionEntry) {
	blocks, sessionID, err := replaySession(m.workspace, entry)
	if err != nil {
		m.appendBlock(block{kind: blockError, text: "load session: " + err.Error()})
		m.refresh()
		return
	}
	m.sessionID = sessionID
	m.toolBlocks = map[string]int{}
	m.blocks = blocks
	m.appendBlock(block{kind: blockDivider, text: "resumed session " + short(sessionID) + " — new prompts continue it"})
	m.refresh()
}

// invokeSkillCommand handles /skill:name [args]: user-invoked skills are
// injected into the prompt (zero model tokens until actually used).
func (m *Model) invokeSkillCommand(input, token string) []tea.Cmd {
	name := strings.TrimPrefix(token, "/skill:")
	var entry *skills.Entry
	for index := range m.skills {
		if m.skills[index].Name == name {
			entry = &m.skills[index]
			break
		}
	}
	if entry == nil {
		m.appendBlock(block{kind: blockError, text: "unknown skill " + name + " (/skills to list, /reload to re-scan)"})
		return nil
	}
	body, err := skills.Body(entry.Path)
	prompt := body
	if err != nil || prompt == "" {
		prompt = "Follow the instructions of the \"" + name + "\" skill."
	}
	if args := strings.TrimSpace(strings.TrimPrefix(input, token)); args != "" {
		prompt += "\n\n" + args
	}
	return m.beginTurn(input, prompt)
}

// beginTurn records the user block and starts one agent turn. It returns the
// commands the caller must pass to the runtime — notably the spinner tick
// seed: the tick chain dies when a turn ends (TickMsg only re-arms while
// running), so every turn start must re-seed it or the status freezes.
func (m *Model) beginTurn(display, prompt string) []tea.Cmd {
	m.appendBlock(block{kind: blockUser, text: display})
	m.running = true
	m.turnVerb = pickVerb()
	m.events = make(chan tea.Msg, 256)
	m.refresh()
	m.startTurn(prompt)
	return []tea.Cmd{m.spinner.Tick}
}

// ---- command table & Command Menu ----

// commandEntry is one static slash command.
type commandEntry struct {
	name string // canonical command; what accepting inserts ("/model")
	args string // argument placeholder shown in menu & help ("" = none)
	desc string
}

// display is the command as shown in the menu and /help.
func (c commandEntry) display() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

// commandTable is the one source of truth for the Command Menu and the
// /help output, so the two can never drift.
var commandTable = []commandEntry{
	{"/new", "", "start a fresh session"},
	{"/resume", "", "pick a previous session"},
	{"/model", "[id]", "show or set the model"},
	{"/reload", "", "re-discover workspace skills & report active config"},
	{"/skills", "", "list discovered skills (model tool / user-invoked)"},
	{"/quit", "", "exit (alias /exit)"},
	{"/help", "", "show commands and keys"},
}

// menuItem is one Command Menu row: a static command or a dynamic skill.
// insert is what tab/enter put into the Prompt Box; display is what the
// menu row shows (insert omits the argument placeholder).
type menuItem struct{ insert, display, desc string }

// menuItems lists the command table plus one /skill:name entry per
// user-invoked skill (model tools are not user-invocable, so they never
// surface here — issue #5).
func (m *Model) menuItems() []menuItem {
	items := make([]menuItem, 0, len(commandTable)+len(m.skills))
	for _, cmd := range commandTable {
		items = append(items, menuItem{insert: cmd.name, display: cmd.display(), desc: cmd.desc})
	}
	for _, entry := range m.skills {
		if !entry.UserOnly {
			continue
		}
		desc := "skill · user-invoked — zero tokens until used"
		if entry.Description != "" {
			desc += " — " + entry.Description
		}
		name := "/skill:" + entry.Name
		items = append(items, menuItem{insert: name, display: name, desc: desc})
	}
	return items
}

// menuMatches returns items whose name starts with the typed command token,
// case-insensitively. The token is the first word of the input ("/RE" →
// /resume); once the user types arguments the menu closes (menuVisible).
func (m *Model) menuMatches() []menuItem {
	input := m.textarea.Value()
	token := strings.TrimPrefix(input, "/")
	if cut := strings.IndexAny(token, " \t\n"); cut >= 0 {
		token = token[:cut]
	}
	token = "/" + strings.ToLower(token)
	var matches []menuItem
	for _, item := range m.menuItems() {
		if strings.HasPrefix(strings.ToLower(item.display), token) {
			matches = append(matches, item)
		}
	}
	return matches
}

// menuVisible reports whether the Command Menu should render above the
// Prompt Box: input starts with "/" and is still selecting a command (no
// whitespace yet), the menu was not Esc-dismissed, and something matches.
func (m Model) menuVisible() bool {
	if m.menuHidden || m.picking {
		return false
	}
	input := m.textarea.Value()
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \t\n") {
		return false
	}
	return len(m.menuMatches()) > 0
}

// handleMenuKeys consumes keys while the Command Menu is open. Locked
// precedence (DESIGN.md): ↑/↓ move the selection (never scroll the
// transcript or move the Prompt Box cursor), tab completes, esc dismisses before
// any quit behavior, and enter accepts the highlighted entry into the input
// WITHOUT sending — a second enter, with the menu closed, sends.
// Returns (handled, cmd).
func (m *Model) handleMenuKeys(msg tea.KeyMsg) (bool, []tea.Cmd) {
	if !m.menuVisible() {
		return false, nil
	}
	matches := m.menuMatches()
	m.clampMenuIndex(matches)
	switch msg.Type {
	case tea.KeyUp:
		if m.menuIndex > 0 {
			m.menuIndex--
		}
		return true, nil
	case tea.KeyDown:
		if m.menuIndex < len(matches)-1 {
			m.menuIndex++
		}
		return true, nil
	case tea.KeyTab:
		m.acceptMenuItem(matches[m.menuIndex])
		return true, nil
	case tea.KeyEsc:
		m.menuHidden = true
		return true, nil
	case tea.KeyEnter:
		if m.running {
			return true, nil
		}
		m.acceptMenuItem(matches[m.menuIndex])
		m.menuHidden = true // a second enter (menu closed) sends
		return true, nil
	}
	return false, nil
}

// acceptMenuItem completes the input to the highlighted command (the
// insertable name — never the displayed argument placeholder) and parks the
// cursor at the end, leaving room to type arguments.
func (m *Model) acceptMenuItem(item menuItem) {
	m.textarea.SetValue(item.insert + " ")
	m.textarea.CursorEnd()
	m.resizeEditor()
}

// clampMenuIndex keeps the selection inside the current match list.
func (m *Model) clampMenuIndex(matches []menuItem) {
	if m.menuIndex >= len(matches) {
		m.menuIndex = 0
	}
}

// setViewportHeight sizes the transcript viewport to whatever the bottom
// section (status, Command Menu, Prompt Box, hint line) actually needs. Nothing
// here may assume a fixed total height: the Command Menu anchors above the
// box and grows it, so the height is always derived from the parts.
func (m *Model) setViewportHeight() {
	m.viewport.Height = max(m.height-2-m.bottomHeight(), 1) // -2: header + its divider
}

// bottomHeight reports the rendered height of everything below the viewport.
// It renders the same parts View() shows, so the two can never drift apart.
func (m Model) bottomHeight() int {
	return lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, m.bottomParts()...))
}

// bottomParts renders everything below the transcript viewport, top to
// bottom: divider, status, Command Menu (when open), Prompt Box, hint line.
// This one list defines both the layout and its height (bottomHeight), so
// the Command Menu can later anchor above the box with no hardcoded total.
func (m Model) bottomParts() []string {
	parts := []string{strings.Repeat("─", max(m.width, 1)), m.statusLine()}
	if m.menuVisible() {
		parts = append(parts, m.viewMenu())
	}
	parts = append(parts, m.renderPromptBox())
	if m.hintsVisible() {
		parts = append(parts, m.renderHints())
	}
	return parts
}

// hintsVisible reports whether the contextual hint line under the Prompt Box
// should render — it hides while a Turn runs so the status area stays clean.
func (m Model) hintsVisible() bool {
	return !m.running
}

func (m *Model) refresh() {
	m.setViewportHeight()
	// Auto-follow: only snap to the bottom when the user is already reading
	// the tail. If they scrolled up, leave their position alone; it re-engages
	// automatically once they page back to the bottom.
	stick := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderTranscript())
	if stick {
		m.viewport.GotoBottom()
	}
}

// discoverSkills scans pi-style default locations plus any -skills extras.
func discoverSkills(workspace string, extras []string) []skills.Entry {
	home, _ := os.UserHomeDir()
	dirs := append(skills.DefaultDirs(workspace, home), extras...)
	return skills.Discover(dirs)
}

// reloadReport re-scans the workspace's discoverable resources and reports
// what the next agent turn will load. The runner subprocess is spawned fresh
// per turn, so discoveries apply automatically; this surfaces them.
func (m *Model) reloadReport() string {
	lines := []string{"reload — active configuration for the next turn:"}
	if m.fileTools {
		lines = append(lines, "  tools: Bash, ViewImage, Read, Write, Edit (file-tools mode)")
	} else {
		lines = append(lines, "  tools: Bash, ViewImage (pristine mode)")
	}
	list := m.skills
	if len(list) == 0 {
		lines = append(lines, "  skills: none found")
	} else {
		var modelTools, userOnly []string
		for _, entry := range list {
			if entry.UserOnly {
				userOnly = append(userOnly, entry.Name)
			} else {
				modelTools = append(modelTools, entry.Name)
			}
		}
		if len(modelTools) > 0 {
			lines = append(lines, "  skills (model tools): "+strings.Join(modelTools, ", ")+" → SkillUse enabled")
		}
		if len(userOnly) > 0 {
			lines = append(lines, "  skills (user-invoked, /skill:name): "+strings.Join(userOnly, ", "))
		}
	}
	lines = append(lines, "  model: "+orDefault(m.model, "(provider default)"))
	return strings.Join(lines, "\n")
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	if m.picking {
		return m.viewPicker()
	}
	modelLabel := orDefault(m.model, "(default model)")
	sessionLabel := "new"
	if m.sessionID != "" {
		sessionLabel = "session " + short(m.sessionID)
	}
	header := titleStyle.Render("unreal-agent") +
		dimStyle.Render("  "+m.workspace+" · "+modelLabel+" · "+sessionLabel)
	return lipgloss.JoinVertical(lipgloss.Left,
		m.truncateToWidth(header),
		strings.Repeat("─", max(m.width, 1)),
		m.viewport.View(),
		lipgloss.JoinVertical(lipgloss.Left, m.bottomParts()...),
	)
}

// toolMode names the active tool mode (glossary: Pristine Mode / File-Tools
// Mode) for the Status Line and reload report.
func (m Model) toolMode() string {
	if m.fileTools {
		return "file-tools"
	}
	return "pristine"
}

// truncateToWidth clips a chrome line to the terminal width so narrow
// terminals never overflow.
func (m Model) truncateToWidth(s string) string {
	return truncate.String(s, uint(max(m.width, 1)))
}

// statusLine renders the working/idle state, the active tool mode, the
// current model id, and per-turn/session token usage. The whole line is
// truncated to the terminal width so narrow terminals never overflow.
func (m Model) statusLine() string {
	var status string
	if m.loading {
		status = statusStyle.Render(m.spinner.View() + " loading sessions…")
	} else if m.running {
		status = statusStyle.Render(m.spinner.View()) + " " +
			dimStyle.Render(orDefault(m.turnVerb, "working…"))
	} else {
		status = dimStyle.Render("idle")
	}
	status += dimStyle.Render("  ·  " + m.toolMode() + "  ·  " + orDefault(m.model, "(default model)"))
	if m.sessionIn > 0 || m.sessionOut > 0 {
		status += dimStyle.Render("  ·  last turn: " + humanCount(m.turnUsage[0]) + " in / " + humanCount(m.turnUsage[1]) + " out")
		status += dimStyle.Render("  ·  session: " + humanCount(m.sessionIn) + " in / " + humanCount(m.sessionOut) + " out")
	}
	return m.truncateToWidth(status)
}

// spinnerVerbs are the randomized gerund verbs shown dim next to the spinner
// while a Turn runs — one is chosen per turn by pickVerb.
var spinnerVerbs = []string{
	"Polishing tarnished generics…",
	"Reticulating splines…",
	"Untangling call stacks…",
	"Sharpening rubber ducks…",
	"Herding semicolons…",
	"Aligning stack frames…",
	"Dusting off the cache…",
	"Consulting the rubber duck…",
	"Compiling courage…",
	"Persuading the linker…",
}

// pickVerb chooses the spinner verb for one Turn.
func pickVerb() string {
	return spinnerVerbs[rand.IntN(len(spinnerVerbs))]
}

// ---- Prompt Box ----

const (
	dimBorder   = lipgloss.Color("8")  // unfocused border
	focusBorder = lipgloss.Color("12") // focused border (accent)
)

// promptBorderStyleFor is the rounded Prompt Box border: dim while
// unfocused, brightening to the accent color on focus.
func promptBorderStyleFor(focused bool) lipgloss.Style {
	border := dimBorder
	if focused {
		border = focusBorder
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)
}

// renderPromptBox draws the rounded Prompt Box around the textarea.
func (m Model) renderPromptBox() string {
	return promptBorderStyleFor(m.textarea.Focused()).Render(m.textarea.View())
}

// hintText is the contextual hint line under the Prompt Box.
const hintText = "shift+enter newline · ctrl+o verbose · /help commands"

// renderHints renders the dim hint line below the box, clipped to the
// terminal width so narrow sizes never wrap or overflow.
func (m Model) renderHints() string {
	return truncate.String(dimStyle.Render(hintText), uint(max(m.width, 1)))
}

// viewMenu renders the Command Menu above the Prompt Box: filtered
// commands, selected row highlighted, (n/total) footer. Height is capped at
// maxMenuRows rows and every row is clipped to the terminal width.
func (m *Model) viewMenu() string {
	matches := m.menuMatches()
	selected := m.menuIndex
	if selected >= len(matches) { // rendering must not write state
		selected = 0
	}
	const maxMenuRows = 8
	var out []string
	for index, item := range matches {
		if index >= maxMenuRows {
			break
		}
		if index == selected {
			out = append(out, statusStyle.Render("▸ "+item.display)+"  "+item.desc)
		} else {
			out = append(out, dimStyle.Render("  "+item.display+"  "+item.desc))
		}
	}
	footer := fmt.Sprintf("(%d/%d)  ↑/↓ select · Tab complete · Enter accept · Esc dismiss", selected+1, len(matches))
	out = append(out, dimStyle.Render(footer))
	for index := range out {
		out[index] = truncate.String(out[index], uint(max(m.width, 1)))
	}
	return strings.Join(out, "\n")
}

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	assistantStyle = lipgloss.NewStyle()
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // ok tool glyphs
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // spinner/menu/picker accent — deliberately the same accent as okStyle; split only intentionally
	diffAddStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	diffDelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// keep sync imported for future shared state; avoids accidental value copies.
