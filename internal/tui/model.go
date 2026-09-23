package tui

import (
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
	rendered string // glamour cache for assistant blocks
	width    int    // width the cache was rendered at
}

type Model struct {
	workspace string
	provider  string
	model     string
	fileTools bool

	viewport      viewport.Model
	textarea      textarea.Model
	spinner       spinner.Model
	blocks        []block
	toolBlocks    map[string]int // callID → blocks index
	skillDirs     []string       // extra -skills directories
	skills        []skills.Entry // discovered skills (refreshed by /reload)
	paletteIndex  int            // command-palette selection
	paletteHidden bool           // Esc-dismissed until input changes
	sessionID     string
	turnUsage     [2]int64 // last turn: in, out tokens
	sessionIn     int64
	sessionOut    int64

	running bool
	aborted bool
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
	ta.Prompt = "❯ "
	ta.CharLimit = 32_000
	ta.SetWidth(80)
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
		m.viewport.Height = msg.Height - 6
		m.textarea.SetWidth(msg.Width)
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
		if handled, extra := m.handlePaletteKeys(msg); handled {
			cmds = append(cmds, extra...)
			break // fall through to the common exit path (re-arms listener)
		}
		if msg.Type != tea.KeyUp && msg.Type != tea.KeyDown && msg.Type != tea.KeyTab {
			// typing/backspace changed the input → reset palette selection
			m.paletteIndex = 0
			m.paletteHidden = false
		}
		switch msg.Type {
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
				m.textarea, _ = m.textarea.Update(msg)
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
	if isScrollKey(message) {
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
		m.appendBlock(block{kind: blockMeta, text: strings.Join([]string{
			"commands:",
			"  /new              start a fresh session",
			"  /resume           pick a previous session",
			"  /model [id]       show or set the model",
			"  /reload           re-discover workspace skills & report active config",
			"  /skills           list discovered skills (model tool / user-invoked)",
			"  /quit             exit (alias /exit)",
			"keys: Enter send · Esc abort/quit · Ctrl+C quit",
		}, "\n")})
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
	m.events = make(chan tea.Msg, 256)
	m.refresh()
	m.startTurn(prompt)
	return []tea.Cmd{m.spinner.Tick}
}

// ---- command palette (pi-style) ----

type paletteItem struct{ name, desc string }

// paletteItems lists built-in commands plus one /skill:name entry per skill.
func (m *Model) paletteItems() []paletteItem {
	items := []paletteItem{
		{"/help", "show commands and keys"},
		{"/new", "start a fresh session"},
		{"/resume", "pick a previous session"},
		{"/model [id]", "show or set the model"},
		{"/skills", "list discovered skills"},
		{"/reload", "re-scan skills & report active config"},
		{"/quit", "exit (alias /exit)"},
	}
	for _, entry := range m.skills {
		kind := "model tool"
		if entry.UserOnly {
			kind = "user-invoked — zero tokens until used"
		}
		desc := "skill · " + kind
		if entry.Description != "" {
			desc += " — " + entry.Description
		}
		items = append(items, paletteItem{name: "/skill:" + entry.Name, desc: desc})
	}
	return items
}

// paletteMatches returns items matching the current input's command token.
func (m *Model) paletteMatches() []paletteItem {
	token := m.textarea.Value()
	token = strings.TrimPrefix(strings.TrimSpace(token), "/")
	if cut := strings.IndexAny(token, " \t"); cut >= 0 {
		token = token[:cut]
	}
	token = strings.ToLower(token)
	var matches []paletteItem
	for _, item := range m.paletteItems() {
		if strings.Contains(strings.ToLower(strings.TrimPrefix(item.name, "/")), token) {
			matches = append(matches, item)
		}
	}
	return matches
}

// paletteVisible reports whether the command palette should render.
func (m *Model) paletteVisible() bool {
	if m.paletteHidden || m.picking || !strings.HasPrefix(strings.TrimSpace(m.textarea.Value()), "/") {
		return false
	}
	return len(m.paletteMatches()) > 0
}

// handlePaletteKeys consumes navigation keys while the palette is open.
// Returns (handled, cmd).
func (m *Model) handlePaletteKeys(msg tea.KeyMsg) (bool, []tea.Cmd) {
	if !m.paletteVisible() {
		return false, nil
	}
	matches := m.paletteMatches()
	if m.paletteIndex >= len(matches) {
		m.paletteIndex = 0
	}
	switch msg.Type {
	case tea.KeyUp:
		if m.paletteIndex > 0 {
			m.paletteIndex--
		}
		return true, nil
	case tea.KeyDown:
		if m.paletteIndex < len(matches)-1 {
			m.paletteIndex++
		}
		return true, nil
	case tea.KeyTab:
		item := matches[m.paletteIndex]
		m.textarea.SetValue(item.name + " ")
		m.paletteHidden = false
		m.resizeEditor()
		return true, nil
	case tea.KeyEsc:
		m.paletteHidden = true
		return true, nil
	case tea.KeyEnter:
		if m.running {
			return true, nil
		}
		item := matches[m.paletteIndex]
		invocation := item.name
		if _, rest, found := strings.Cut(m.textarea.Value(), " "); found && strings.TrimSpace(rest) != "" {
			invocation += " " + strings.TrimSpace(rest)
		}
		m.textarea.Reset()
		m.textarea.SetHeight(1)
		m.paletteHidden = true
		m.paletteIndex = 0
		return true, m.command(invocation)
	}
	return false, nil
}

func (m *Model) refresh() {
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
	status := ""
	if m.loading {
		status = statusStyle.Render(m.spinner.View() + " loading sessions…")
	} else if m.running {
		status = statusStyle.Render(m.spinner.View() + " working…")
	} else {
		status = dimStyle.Render("idle")
		if m.sessionIn > 0 || m.sessionOut > 0 {
			status += dimStyle.Render("  last turn: " + humanCount(m.turnUsage[0]) + " in / " + humanCount(m.turnUsage[1]) + " out")
			status += dimStyle.Render("  ·  session: " + humanCount(m.sessionIn) + " in / " + humanCount(m.sessionOut) + " out")
		}
	}
	bottom := []string{strings.Repeat("─", max(m.width, 1)), status}
	if m.paletteVisible() {
		bottom = append(bottom, m.viewPalette())
	}
	bottom = append(bottom, m.textarea.View())
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		strings.Repeat("─", max(m.width, 1)),
		m.viewport.View(),
		lipgloss.JoinVertical(lipgloss.Left, bottom...),
	)
}

// viewPalette renders the pi-style command palette above the input:
// filtered commands, selected row highlighted, (n/total) counter.
func (m *Model) viewPalette() string {
	matches := m.paletteMatches()
	if m.paletteIndex >= len(matches) {
		m.paletteIndex = 0
	}
	const maxShown = 8
	var out []string
	for index, item := range matches {
		if index >= maxShown {
			break
		}
		line := "  " + item.name + "  " + item.desc
		if index == m.paletteIndex {
			out = append(out, statusStyle.Render("▸ "+item.name)+"  "+item.desc)
		} else {
			out = append(out, dimStyle.Render(line))
		}
	}
	out = append(out, dimStyle.Render(fmt.Sprintf("(%d/%d)  ↑/↓ select · Tab complete · Enter invoke · Esc dismiss", m.paletteIndex+1, len(matches))))
	return strings.Join(out, "\n")
}

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	assistantStyle = lipgloss.NewStyle()
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	diffAddStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	diffDelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// keep sync imported for future shared state; avoids accidental value copies.
