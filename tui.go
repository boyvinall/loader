package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	statusTickInterval = 250 * time.Millisecond
	logLineCap         = 5000
	logDrainMax        = 256

	minSidebarWidth = 20
	maxSidebarWidth = 40

	// lipgloss adds these on top of a style's content Width/Height.
	panelBorderPaddingWidth  = 4 // 1 border + 1 padding, each side
	panelBorderPaddingHeight = 2 // 1 border, top and bottom
)

var (
	colorBorder = lipgloss.Color("12") // blue
	colorTitle  = lipgloss.Color("11") // yellow

	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(colorTitle)
	styleFooter = lipgloss.NewStyle().Bold(true).Foreground(colorTitle)
	styleBorder = lipgloss.NewStyle().Foreground(colorBorder)
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleFail   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	styleSystem = lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	styleStdout = lipgloss.NewStyle().Foreground(lipgloss.Color("7")) // white
	styleFaint  = lipgloss.NewStyle().Faint(true)

	panelBorder = lipgloss.RoundedBorder()
)

// runTUI drives an Engine with a fullscreen bubbletea dashboard. Used when
// stdout/stderr is an interactive terminal.
func runTUI(cfg Config) error {
	eng := NewEngine(cfg)

	// bubbletea's default signal handler intercepts an external SIGINT
	// itself and quits immediately, before Update ever sees it — bypassing
	// StopLaunching/KillRunning and leaving already-launched processes
	// running. Disable it and handle SIGINT ourselves instead, mirroring
	// plain.go's two-stage Ctrl-C behavior.
	p := tea.NewProgram(newModel(eng, cfg), tea.WithAltScreen(), tea.WithoutSignalHandler())

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Messages for each stage are shown in the footer instead (see
	// renderFooter), so nothing extra happens here on stage transitions.
	eng.WatchInterrupts(sigCh, nil, nil)

	_, err := p.Run()
	return err
}

// Messages

type statusTickMsg time.Time

type logBatchMsg []LogLine

type logChClosedMsg struct{}

type engineFinishedMsg struct{}

func statusTickCmd() tea.Cmd {
	return tea.Tick(statusTickInterval, func(t time.Time) tea.Msg { return statusTickMsg(t) })
}

func runEngineCmd(eng *Engine) tea.Cmd {
	return func() tea.Msg {
		eng.Run()
		return engineFinishedMsg{}
	}
}

// waitForLogLines blocks for the next log line, then drains any further
// lines already available (up to logDrainMax) so a burst of output becomes
// one Update/View cycle instead of one per line.
func waitForLogLines(eng *Engine) tea.Cmd {
	return func() tea.Msg {
		ch := eng.LogLines()
		first, ok := <-ch
		if !ok {
			return logChClosedMsg{}
		}
		lines := []LogLine{first}
		for len(lines) < logDrainMax {
			select {
			case l, ok := <-ch:
				if !ok {
					return logBatchMsg(lines)
				}
				lines = append(lines, l)
			default:
				return logBatchMsg(lines)
			}
		}
		return logBatchMsg(lines)
	}
}

// Model

type Model struct {
	eng *Engine
	cfg Config

	width, height int
	ready         bool

	sidebarWidth       int // shared by Latency (top row) and Activity (body row) — double the base column width, to fit Latency's OK/FAIL columns
	logWidth           int
	statusWidth        int // left half of the top row's Status+Config width
	configWidth        int // right half of the top row's Status+Config width
	topRowHeight       int // shared by the Status, Config and Latency panels
	bodyHeight         int
	runningOuterHeight int
	logPanelHeight     int

	viewport   viewport.Model
	progress   progress.Model
	followTail bool
	logLines   []string

	snap Snapshot
}

func newModel(eng *Engine, cfg Config) Model {
	m := Model{
		eng:        eng,
		cfg:        cfg,
		width:      80,
		height:     24,
		viewport:   viewport.New(0, 0),
		progress:   progress.New(progress.WithDefaultGradient()),
		followTail: true,
		snap:       eng.Snapshot(),
	}
	m.recalcSizes()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		runEngineCmd(m.eng),
		waitForLogLines(m.eng),
		statusTickCmd(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.recalcSizes()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case statusTickMsg:
		m.snap = m.eng.Snapshot()
		if m.eng.Stage() == StageFinished {
			return m, nil
		}
		return m, statusTickCmd()

	case logBatchMsg:
		for _, l := range msg {
			m.logLines = append(m.logLines, formatLogLine(l))
		}
		if len(m.logLines) > logLineCap {
			m.logLines = m.logLines[len(m.logLines)-logLineCap:]
		}
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		if m.followTail {
			m.viewport.GotoBottom()
		}
		return m, waitForLogLines(m.eng)

	case logChClosedMsg:
		return m, nil

	case engineFinishedMsg:
		m.snap = m.eng.FinalSnapshot()
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Once finished, q or ctrl+c exits.
	if m.eng.Stage() == StageFinished {
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if isScrollUpKey(msg) {
			m.followTail = false
		}
		if m.viewport.AtBottom() {
			m.followTail = true
		}
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q":
		switch m.eng.Stage() {
		case StageRunning:
			m.eng.StopLaunching()
		case StageStopping:
			m.eng.KillRunning()
		case StageKilling:
			// Safety valve: don't let the UI feel stuck if killed
			// processes are slow to exit.
			return m, tea.Quit
		}
		return m, nil

	case "end", "G":
		m.followTail = true
		m.viewport.GotoBottom()
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	if isScrollUpKey(msg) {
		m.followTail = false
	}
	if m.viewport.AtBottom() {
		m.followTail = true
	}
	return m, cmd
}

func isScrollUpKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k", "pgup", "b", "u":
		return true
	default:
		return false
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// truncateEllipsis clips s to at most width display columns, replacing any
// clipped tail with "…" so an overlong value can't wrap and grow a
// fixed-height panel.
func truncateEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	r := []rune(s)
	if len(r) > width-1 {
		r = r[:width-1]
	}
	return string(r) + "…"
}

// recalcSizes computes panel dimensions from the current terminal size. It's
// called once at construction (with a default 80x24) and again on every
// tea.WindowSizeMsg.
func (m *Model) recalcSizes() {
	const footerHeight = 1
	const latencyContentRows = 8 // header row (OK/FAIL) + one stat per line: samples, min, avg, p50, p95, p99, max

	// The Latency panel is twice as wide as a base sidebar column, to fit
	// its OK/FAIL columns; the Activity panel below it matches that width
	// too. Status+Config (top row) and Running+Log (body row) give up that
	// extra width so both rows still total exactly m.width.
	m.sidebarWidth = clampInt(m.width/3, minSidebarWidth, maxSidebarWidth)
	topLogWidth := clampInt(m.width-m.sidebarWidth, 2, m.width)
	m.statusWidth = topLogWidth / 2
	m.configWidth = topLogWidth - m.statusWidth
	m.logWidth = topLogWidth

	// Top row: Status, Config and Latency side by side, sharing a height tall
	// enough for whichever needs more room.
	statusOuter := m.statusContentLines() + panelBorderPaddingHeight
	configOuter := m.configContentLines() + panelBorderPaddingHeight
	latencyOuter := latencyContentRows + panelBorderPaddingHeight
	m.topRowHeight = max(statusOuter, configOuter, latencyOuter)

	m.bodyHeight = clampInt(m.height-m.topRowHeight-footerHeight, 3, m.height)

	// Left column: a running-processes panel above the log panel, which
	// takes whatever's left. The running panel wants enough content rows to
	// list every parallel slot plus one (so a full house doesn't look
	// clipped), but is capped at half the body height so it never grows
	// taller than the log panel — in extreme cases (high --max-parallel or a
	// small terminal) the two end up the same size.
	desiredRunningOuter := m.cfg.MaxParallel + 1 + panelBorderPaddingHeight
	m.runningOuterHeight = clampInt(desiredRunningOuter, 1, max(m.bodyHeight/2, 1))
	m.logPanelHeight = clampInt(m.bodyHeight-m.runningOuterHeight, 3, m.bodyHeight)

	logContentHeight := clampInt(m.logPanelHeight-panelBorderPaddingHeight, 1, m.logPanelHeight)
	m.viewport.Width = clampInt(m.logWidth-panelBorderPaddingWidth, 1, m.logWidth)
	m.viewport.Height = logContentHeight

	m.progress.Width = clampInt(m.statusWidth-panelBorderPaddingWidth-4, 10, 200)
}

// statusContentLines returns the number of content rows in the Status panel:
// running, completed, failed, elapsed, plus an optional progress bar.
func (m Model) statusContentLines() int {
	n := 4
	if _, ok := progressFraction(m.cfg, m.snap); ok {
		n++
	}
	return n
}

// configContentLines returns the number of content rows in the Config panel:
// command, rate, max parallel, max count, max duration.
func (m Model) configContentLines() int {
	return 5
}

// progressFraction reports how far a run is toward stopping, as the larger
// of "fraction of max-count launched" and "fraction of duration elapsed" —
// matching the actual stop condition, which fires on whichever limit is hit
// first. ok is false when neither limit is configured, in which case no
// progress bar is shown.
func progressFraction(cfg Config, snap Snapshot) (frac float64, ok bool) {
	if cfg.MaxCount <= 0 && cfg.TestDuration <= 0 {
		return 0, false
	}
	if cfg.MaxCount > 0 {
		frac = max(frac, min(1, float64(snap.Launched)/float64(cfg.MaxCount)))
	}
	if cfg.TestDuration > 0 {
		frac = max(frac, min(1, float64(snap.Elapsed)/float64(cfg.TestDuration)))
	}
	return frac, true
}

func formatLogLine(l LogLine) string {
	prefix := fmt.Sprintf("[%d] ", l.ProcID)
	switch l.Stream {
	case "system":
		return styleSystem.Render(prefix + l.Text)
	case "stderr":
		return styleFail.Render(prefix + l.Text)
	default:
		return styleStdout.Render(prefix + l.Text)
	}
}

// View

func (m Model) View() string {
	if !m.ready {
		return "starting…"
	}
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, m.renderStatusPanel(), m.renderConfigPanel(), m.renderLatencyPanel())
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.JoinVertical(lipgloss.Left, m.renderRunningPanel(), m.renderLogPanel()),
		m.renderActivityPanel(),
	)
	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, topRow, body, footer)
}

// renderPanel draws body inside a rounded, blue-bordered box sized to
// outerWidth x outerHeight. If title is non-empty, it's inlaid in the top
// border (bold, white) instead of taking up a line of content.
func renderPanel(title string, outerWidth, outerHeight int, body string) string {
	// lipgloss's Width/Height already include padding in their budget — only
	// the border is added on top of them — so subtract just the border here,
	// not border+padding (that's panelBorderPaddingWidth/Height, used
	// elsewhere for the plain text content area inside the padding).
	const borderWidth, borderHeight = 2, 2
	styleWidth := clampInt(outerWidth-borderWidth, 1, outerWidth)
	styleHeight := clampInt(outerHeight-borderHeight, 1, outerHeight)

	box := lipgloss.NewStyle().
		Border(panelBorder, false, true, true, true). // no top: the title bar replaces it
		BorderForeground(colorBorder).
		Padding(0, 1).
		Width(styleWidth).
		Height(styleHeight).
		Render(body)

	return renderTopBorder(title, outerWidth) + "\n" + box
}

// renderTopBorder draws a top border line of exactly outerWidth columns,
// inlaying title (if any) after the left corner, e.g. "╭─ Title ────╮".
func renderTopBorder(title string, outerWidth int) string {
	corner, dash, endCorner := panelBorder.TopLeft, panelBorder.Top, panelBorder.TopRight

	if title == "" {
		return styleBorder.Render(corner + strings.Repeat(dash, max(outerWidth-2, 0)) + endCorner)
	}

	label := " " + title + " "
	remaining := max(outerWidth-2-1-lipgloss.Width(label), 0) // corners(2) + leading dash(1) + label

	var b strings.Builder
	b.WriteString(styleBorder.Render(corner + dash))
	b.WriteString(styleTitle.Render(label))
	b.WriteString(styleBorder.Render(strings.Repeat(dash, remaining) + endCorner))
	return b.String()
}

// renderStatusPanel shows live run state: running/completed/failed counts,
// elapsed time, and (when a limit is configured) a progress bar.
func (m Model) renderStatusPanel() string {
	// Clamp every line to the panel's content width so a narrow terminal
	// can't wrap a label+value line and grow the panel past topRowHeight —
	// same trap as the Config panel (see renderConfigPanel).
	avail := clampInt(m.statusWidth-panelBorderPaddingWidth, 0, 1000)
	row := func(label, value string) string {
		return truncateEllipsis(fmt.Sprintf("%-14s%s", label, value), avail)
	}

	failed := row("failed:", fmt.Sprintf("%d", m.snap.Failed))
	if m.snap.Failed > 0 {
		failed = styleFail.Render(failed)
	}

	const statLines = 4 // running, completed, failed, elapsed
	lines := []string{
		row("running:", fmt.Sprintf("%d", m.snap.Running)),
		row("completed:", fmt.Sprintf("%d", m.snap.Completed)),
		failed,
		row("elapsed:", m.snap.Elapsed.Round(time.Second).String()),
	}

	var b strings.Builder
	b.WriteString(strings.Join(lines, "\n"))

	if frac, ok := progressFraction(m.cfg, m.snap); ok {
		// Anchor the bar to the panel's bottom content row, padding with
		// blank lines to fill whatever extra height the Latency panel (which
		// can be taller) has forced onto this row.
		innerHeight := clampInt(m.topRowHeight-panelBorderPaddingHeight, statLines+1, 1000)
		b.WriteString(strings.Repeat("\n", innerHeight-statLines))
		b.WriteString(m.progress.ViewAs(frac))
	}

	return renderPanel("Status", m.statusWidth, m.topRowHeight, b.String())
}

// renderConfigPanel shows the static settings the run was launched with.
func (m Model) renderConfigPanel() string {
	maxCount := "unlimited"
	if m.cfg.MaxCount > 0 {
		maxCount = fmt.Sprintf("%d", m.cfg.MaxCount)
	}
	maxDuration := "unlimited"
	if m.cfg.TestDuration > 0 {
		maxDuration = m.cfg.TestDuration.String()
	}

	// Every line — not just the command, which can be arbitrarily long — is
	// clipped to the panel's content width so a long duration/count or a
	// narrow terminal can't wrap a line and grow the panel past
	// topRowHeight (recalcSizes sizes this panel assuming exactly
	// configContentLines() lines; a wrapped line breaks that assumption and
	// pushes every panel below it down by a row).
	//
	// Labels are padded to a wider column (17) than the Status panel's (14)
	// since the longest one here ("max parallel:"/"max duration:") needs
	// room to breathe too — this keeps the tightest gap on either panel
	// comparable.
	avail := clampInt(m.configWidth-panelBorderPaddingWidth, 0, 1000)
	row := func(label, value string) string {
		return truncateEllipsis(fmt.Sprintf("%-17s%s", label, value), avail)
	}

	lines := []string{
		row("command:", strings.Join(m.cfg.Args, " ")),
		row("rate:", m.cfg.Rate.String()),
		row("max parallel:", fmt.Sprintf("%d", m.cfg.MaxParallel)),
		row("max count:", maxCount),
		row("max duration:", maxDuration),
	}

	return renderPanel("Config", m.configWidth, m.topRowHeight, strings.Join(lines, "\n"))
}

func (m Model) renderRunningPanel() string {
	total := len(m.snap.RunningProcs)
	maxRows := clampInt(m.runningOuterHeight-panelBorderPaddingHeight, 0, total)

	var b strings.Builder
	for i := range maxRows {
		e := m.snap.RunningProcs[i]
		fmt.Fprintf(&b, "#%-6d %v\n", e.Index, e.Elapsed.Round(time.Second))
	}
	if total == 0 {
		b.WriteString(styleFaint.Render("(none)"))
	}

	title := fmt.Sprintf("Running (%d)", total)
	if total > maxRows {
		title = fmt.Sprintf("Running (%d, showing %d)", total, maxRows)
	}

	return renderPanel(title, m.logWidth, m.runningOuterHeight, strings.TrimRight(b.String(), "\n"))
}

func (m Model) renderLogPanel() string {
	title := "Log Output"
	if m.snap.DroppedLogLines > 0 {
		title = fmt.Sprintf("Log Output (%d dropped)", m.snap.DroppedLogLines)
	}
	return renderPanel(title, m.logWidth, m.logPanelHeight, m.viewport.View())
}

// latencyCell formats a single percentile value, or "-" if p has no samples
// to have computed it from.
func latencyCell(p Percentiles, v time.Duration) string {
	if p.Count == 0 {
		return "-"
	}
	return v.Round(time.Millisecond).String()
}

func (m Model) renderLatencyPanel() string {
	ok, fail := m.snap.PercentilesOK, m.snap.PercentilesFail

	if ok.Count == 0 && fail.Count == 0 {
		return renderPanel("Latency", m.sidebarWidth, m.topRowHeight, styleFaint.Render("(no samples yet)"))
	}

	// Two value columns (OK, FAIL) after the label, sized to fit the values
	// themselves ("12.345ms", "FAIL") rather than stretching to share the
	// panel's doubled width evenly — that left a wide gap between them on
	// anything but the narrowest terminal.
	const labelWidth = 11 // "samples: "
	colWidth := clampInt((m.sidebarWidth-panelBorderPaddingWidth-labelWidth)/2, 7, 10)
	label := lipgloss.NewStyle().Width(labelWidth)
	col := lipgloss.NewStyle().Width(colWidth)

	row := func(name, okVal, failVal string) string {
		return label.Render(name) + col.Render(okVal) + failVal
	}

	var b strings.Builder
	fmt.Fprintln(&b, row("", styleOK.Render("OK"), styleFail.Render("FAIL")))
	fmt.Fprintln(&b, row("samples:", fmt.Sprintf("%d", ok.Count), fmt.Sprintf("%d", fail.Count)))
	fmt.Fprintln(&b, row("min:", latencyCell(ok, ok.Min), latencyCell(fail, fail.Min)))
	fmt.Fprintln(&b, row("avg:", latencyCell(ok, ok.Avg), latencyCell(fail, fail.Avg)))
	fmt.Fprintln(&b, row("p50:", latencyCell(ok, ok.P50), latencyCell(fail, fail.P50)))
	fmt.Fprintln(&b, row("p95:", latencyCell(ok, ok.P95), latencyCell(fail, fail.P95)))
	fmt.Fprintln(&b, row("p99:", latencyCell(ok, ok.P99), latencyCell(fail, fail.P99)))
	fmt.Fprint(&b, row("max:", latencyCell(ok, ok.Max), latencyCell(fail, fail.Max)))

	return renderPanel("Latency", m.sidebarWidth, m.topRowHeight, b.String())
}

func (m Model) renderActivityPanel() string {
	var b strings.Builder

	maxRows := clampInt(m.bodyHeight-panelBorderPaddingHeight, 0, len(m.snap.Recent))

	// The status column is fixed-width and colored, so it's rendered
	// separately from the rest of the row: truncateEllipsis isn't
	// ANSI-aware, so it must never be applied to styled text, only to the
	// plain-text prefix ahead of it. Reserving the status column's width
	// upfront guarantees the whole row fits within the panel even at the
	// narrowest sidebar width, so it can never wrap.
	const statusColWidth = 4 // "FAIL" is the longest status
	statusCol := lipgloss.NewStyle().Width(statusColWidth)
	prefixAvail := clampInt(m.sidebarWidth-panelBorderPaddingWidth-statusColWidth-1, 0, 1000)

	recent := m.snap.Recent
	for i, rows := len(recent)-1, 0; i >= 0 && rows < maxRows; i, rows = i-1, rows+1 {
		e := recent[i]

		var status, dur string
		switch e.Kind {
		case ActivityOK:
			status = statusCol.Render(styleOK.Render("OK"))
			dur = e.Duration.Round(time.Millisecond).String()
		case ActivityFail:
			status = statusCol.Render(styleFail.Render("FAIL"))
			dur = e.Duration.Round(time.Millisecond).String()
		default: // ActivityStarted
			status = statusCol.Render(styleSystem.Render("RUN"))
			dur = "-"
		}

		prefix := fmt.Sprintf("%s   #%-6d %v", e.Time.Format(time.TimeOnly), e.Index, dur)
		prefix = truncateEllipsis(prefix, prefixAvail)
		fmt.Fprintf(&b, "%-*s %s\n", prefixAvail, prefix, status)
	}

	return renderPanel("Recent Activity", m.sidebarWidth, m.bodyHeight, strings.TrimRight(b.String(), "\n"))
}

func (m Model) renderFooter() string {
	var hint string
	switch m.eng.Stage() {
	case StageRunning:
		hint = "ctrl+c/q: stop launching  •  ↑/↓ pgup/pgdn: scroll log  •  end/G: jump to tail"
	case StageStopping:
		hint = "waiting for running processes to finish  •  ctrl+c/q: kill now"
	case StageKilling:
		hint = "killing running processes…  •  ctrl+c/q: force quit"
	case StageFinished:
		hint = "finished — q: exit  •  ↑/↓ pgup/pgdn: scroll log"
	}
	return styleFooter.Width(m.width).Render(hint)
}
