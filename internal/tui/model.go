package tui

import (
	"math/rand/v2"
	"os"
	"path/filepath"

	"fmt"
	"strings"

	"unreal-agent-tui/internal/catalog"
	"unreal-agent-tui/internal/settings"
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
	blockHeader
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
	cfg       settings.Settings // resolved configuration (settings file precedence already applied)
	turnVerb  string            // spinner verb chosen for the running Turn

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
	// Usage Line inputs (glossary: Usage Line). Session buckets accumulate
	// over every turn; the turn buckets keep the latest turn's values for
	// CH and the context percentage.
	sessionIn         int64
	sessionOut        int64
	sessionCached     int64 // cache-read total
	sessionCacheWrite int64 // cache-write total
	turnIn            int64 // latest turn: full prompt tokens (inclusive)
	turnOut           int64
	turnCached        int64 // latest turn: cache-read
	turnCacheWrite    int64 // latest turn: cache-write

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

func New(workspace string, cfg settings.Settings, skillDirs []string) Model {
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		absolute = workspace
	}
	ta := textarea.New()
	// The Composer draws its own accent ❯ prompt; the textarea itself stays
	// bare — no prompt glyph, no line-number gutter, no placeholder — and
	// no cursor-line background: bubbles' focused style paints a black
	// background across the cursor line, which renders as a bar over the
	// terminal's own background.
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.CharLimit = 32_000
	// 78 = 80-wide terminal minus the two columns of the ❯ prompt.
	ta.SetWidth(78)
	ta.SetHeight(1)
	ta.Focus()

	m := Model{
		workspace:  absolute,
		cfg:        cfg,
		skillDirs:  skillDirs,
		skills:     discoverSkills(absolute, skillDirs),
		textarea:   ta,
		viewport:   viewport.New(80, 20),
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		toolBlocks: map[string]int{},
	}
	// A fresh session opens with the Header as the transcript's welcome
	// block: visible at launch, scrolling away with the conversation.
	// Resumed sessions replace the blocks wholesale, so no header there.
	m.appendBlock(block{kind: blockHeader})
	return m
}

func (m Model) Init() tea.Cmd {
	// Boot into a fresh session: the transcript opens with the Header as
	// its welcome block; the launcher stays on /resume.
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
		in         int64
		out        int64
		cached     int64 // cache-read tokens
		cacheWrite int64 // cache-write tokens
	}
)

// ---- update ----

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.textarea.SetWidth(max(msg.Width-2, 1)) // -2: ❯ prompt columns
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
		if m.cfg.Model == "" {
			m.cfg.Model = msg.model
		}
		m.appendBlock(block{kind: blockMeta, text: "session " + short(msg.sessionID) + " · " + msg.model + " · " + m.workspace})
	case usageMsg:
		m.turnIn, m.turnOut = msg.in, msg.out
		m.turnCached, m.turnCacheWrite = msg.cached, msg.cacheWrite
		m.sessionIn += msg.in
		m.sessionOut += msg.out
		m.sessionCached += msg.cached
		m.sessionCacheWrite += msg.cacheWrite
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
	case csiSequenceMsg:
		// Disambiguated keys bubbletea v1 can't parse; only the modified
		// enters matter (shift+enter newline, alt+enter fallback).
		if isModifiedEnter(msg) {
			m.insertNewline()
		}
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
			// The per-block render cache is keyed on the verbose flag, so
			// dual-rendered blocks re-render lazily — no cache wipe needed.
			m.verbose = !m.verbose
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
				m.insertNewline()
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
		m.appendBlock(block{kind: blockHeader})
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
			m.appendBlock(block{kind: blockMeta, text: "model: " + orDefault(m.cfg.Model, "(provider default)")})
		} else {
			m.cfg.Model = fields[1]
			m.appendBlock(block{kind: blockMeta, text: "model set to " + m.cfg.Model})
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
// insert is what tab/enter put into the Composer; display is what the
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
		// No redundant boilerplate: /skill:name already says what it is,
		// so the description is just the skill's own description.
		name := "/skill:" + entry.Name
		items = append(items, menuItem{insert: name, display: name, desc: entry.Description})
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
// Composer: input starts with "/" and is still selecting a command (no
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
// transcript or move the Composer cursor), tab completes into the Composer,
// esc dismisses before any quit behavior, and enter accepts = runs the
// highlighted entry immediately (no prefill, no second enter).
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
		// Complete: fill the highlighted entry's insertable name into the
		// Composer with a trailing space, leaving room to type arguments.
		item := matches[m.menuIndex]
		m.textarea.SetValue(item.insert + " ")
		m.textarea.CursorEnd()
		m.resizeEditor()
		return true, nil
	case tea.KeyEsc:
		m.menuHidden = true
		return true, nil
	case tea.KeyEnter:
		if m.running {
			return true, nil
		}
		// Enter accepts = runs the highlighted entry immediately; Tab is the
		// complete action (insert into the Composer). No prefill, no second
		// enter.
		item := matches[m.menuIndex]
		m.textarea.Reset()
		m.textarea.SetHeight(1)
		m.menuHidden = true
		return true, m.command(item.insert)
	}
	return false, nil
}

// clampIndex keeps a selection inside [0, n). Pure, so rendering can clamp
// without writing state.
func clampIndex(index, n int) int {
	if index >= n {
		return 0
	}
	return index
}

// clampMenuIndex keeps the selection inside the current match list.
func (m *Model) clampMenuIndex(matches []menuItem) {
	m.menuIndex = clampIndex(m.menuIndex, len(matches))
}

// setViewportHeight sizes the transcript viewport to whatever the bottom
// section (status, Command Menu, Composer) actually needs. Nothing here may
// assume a fixed total height: the Command Menu anchors above the Composer
// and grows the bottom section, so the height is always derived from the
// parts.
func (m *Model) setViewportHeight() {
	m.viewport.Height = max(m.height-m.bottomHeight(), 1)
}

// bottomHeight reports the rendered height of everything below the viewport.
// It renders the same parts View() shows, so the two can never drift apart.
func (m Model) bottomHeight() int {
	return lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, m.bottomParts()...))
}

// bottomParts renders everything below the transcript viewport, top to
// bottom: Command Menu (when open), Composer, Usage Line. This one list
// defines both the layout and its height (bottomHeight), so the Command
// Menu can anchor above the Composer and the Usage Line can sit below it
// with no hardcoded total.
func (m Model) bottomParts() []string {
	var parts []string
	// The Command Menu and the launcher share the same anchor: the slot
	// directly above the Composer. At most one is open at a time.
	if m.picking {
		parts = append(parts, m.viewPicker())
	} else if m.menuVisible() {
		parts = append(parts, m.viewMenu())
	}
	parts = append(parts, m.renderComposer())
	parts = append(parts, m.usageLine())
	return parts
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
	lines = append(lines, "  tools: Bash, ViewImage, Read, Write, Edit")
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
	lines = append(lines, "  model: "+orDefault(m.cfg.Model, "(provider default)"))
	return strings.Join(lines, "\n")
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	bottom := lipgloss.JoinVertical(lipgloss.Left, m.bottomParts()...)
	// One layout for both states: the transcript viewport fills the space
	// above the bottom block, so the Composer stays grounded at the bottom
	// of the terminal. The launcher and the Command Menu open as bottom
	// parts above the Composer — chrome never moves.
	return lipgloss.JoinVertical(lipgloss.Left, m.viewport.View(), bottom)
}

// insertNewline adds a newline at the cursor and re-flows the Composer.
// The textarea's InsertNewline binding only matches bare "enter", so a
// modified enter (shift/alt, raw or CSI-encoded) arrives unmatched and would
// be swallowed — insert the newline explicitly.
func (m *Model) insertNewline() {
	m.textarea.InsertString("\n")
	m.resizeEditor()
}

// truncateToWidth clips a chrome line to the terminal width so narrow
// terminals never overflow.
func (m Model) truncateToWidth(s string) string {
	return truncate.String(s, uint(max(m.width, 1)))
}

// truncateToWidthTail clips a chrome line to the terminal width, marking
// the cut with an ellipsis so long rows never end mid-word.
func (m Model) truncateToWidthTail(s string) string {
	return truncate.StringWithTail(s, uint(max(m.width, 1)), "...")
}

// ---- Header ----

// logoArt is zua's pixel-mascot: an original blocky critter drawn from
// solid █ cells. The eye and leg gaps are the terminal's own background
// showing through, the same trick the reference launcher's mascot uses.
const logoArt = `████████████
██  ████  ██
████████████
  ██████████
  ██████████
  ██    ██
 ████  ████`

// headerStats is the launcher's live stats line: model · thinking level ·
// skills count. It reads current state on every render, so /reload's
// re-discovery shows up the next time the launcher opens. The model is
// prettified here only (catalog display name, raw fallback); the Usage
// Line keeps the raw id, matching pi.
func (m Model) headerStats() string {
	return orDefault(catalog.Prettify(m.cfg.Model), "(default model)") +
		" · " + orDefault(m.cfg.ThinkingLevel, "high") +
		" · " + itoa(int64(len(m.skills))) + " skills"
}

// renderHeader draws the launcher Header: the zua logo art next to the app
// name, the workspace path, and the stats line. Every line clips to the
// terminal width so narrow terminals never wrap or overflow.
func (m Model) renderHeader() string {
	logo := titleStyle.Render(logoArt)
	right := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("zua"),
		dimStyle.Render(m.workspace),
		dimStyle.Render(m.headerStats()),
	)
	header := lipgloss.JoinHorizontal(lipgloss.Top, logo, "  ", right)
	lines := strings.Split(header, "\n")
	for index := range lines {
		lines[index] = m.truncateToWidth(lines[index])
	}
	return strings.Join(lines, "\n")
}

// ---- spinner verbs ----

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

// ---- Composer ----

const (
	accentColor = lipgloss.Color("12") // the single accent
	dimColor    = lipgloss.Color("8")  // dim gray
)

// composerPromptStyle is the accent ❯ prompt; composerRuleStyle is the dim
// thin rule above and below the input (glossary: Composer).
var (
	composerPromptStyle = lipgloss.NewStyle().Foreground(accentColor)
	composerRuleStyle   = lipgloss.NewStyle().Foreground(dimColor)
)

// renderComposer draws the Composer: a thin rule above, the accent ❯ prompt
// with the input, and a rule below — no border box, no placeholder, no
// hint line.
func (m Model) renderComposer() string {
	rule := m.truncateToWidth(composerRuleStyle.Render(strings.Repeat("─", max(m.width, 1))))
	line := composerPromptStyle.Render("❯ ") + m.textarea.View()
	return lipgloss.JoinVertical(lipgloss.Left, rule, line, rule)
}

// viewMenu renders the Command Menu above the Composer: filtered
// commands, selected row highlighted, (n/total) footer. Height is capped at
// maxMenuRows rows and every row is clipped to the terminal width.
func (m *Model) viewMenu() string {
	matches := m.menuMatches()
	selected := clampIndex(m.menuIndex, len(matches)) // rendering must not write state
	const maxMenuRows = 8
	var out []string
	// Scrolling window: the 8 visible rows follow the selection when the
	// match list outgrows the cap, like the session picker's window.
	start := 0
	if len(matches) > maxMenuRows {
		start = selected - maxMenuRows/2
		if start < 0 {
			start = 0
		}
		if maxStart := len(matches) - maxMenuRows; start > maxStart {
			start = maxStart
		}
	}
	for index := start; index < len(matches) && index < start+maxMenuRows; index++ {
		item := matches[index]
		// The color highlight marks the selected row — no glyph — but the
		// accent covers only the name: the description stays plain, so it
		// reads at normal weight next to the accented command.
		if index == selected {
			out = append(out, "  "+statusStyle.Render(item.display)+"  "+item.desc)
		} else {
			out = append(out, dimStyle.Render("  "+item.display+"  "+item.desc))
		}
	}
	footer := fmt.Sprintf("(%d/%d)  ↑/↓ select · Tab complete · Enter accept · Esc dismiss", selected+1, len(matches))
	// A blank line keeps the footer visually apart from the rows; its
	// indent aligns it with the rows above.
	out = append(out, "", dimStyle.Render("  "+footer))
	for index := range out {
		out[index] = m.truncateToWidthTail(out[index])
	}
	return strings.Join(out, "\n")
}

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	assistantStyle = lipgloss.NewStyle()
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	warnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // context warning past 70 percent
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // ok tool glyphs
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // spinner/menu/picker accent; same accent as okStyle on purpose — split deliberately if their needs diverge
	diffAddStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	diffDelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// keep sync imported for future shared state; avoids accidental value copies.
