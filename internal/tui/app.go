package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"beautything/internal/render"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StreamEvent is a formatted event plus stream metadata for the TUI.
type StreamEvent struct {
	Entry render.Entry
	At    time.Time
	Gap   bool
}

// Config configures the BeautyThing terminal UI.
type Config struct {
	SourceURL string
	DiskOnly  bool
	NoColor   bool
	StartID   int64
	MaxEvents int
}

type streamClosedMsg struct{}
type tickMsg time.Time
type streamErrMsg struct{ err error }

type model struct {
	cfg  Config
	msgs <-chan tea.Msg

	width  int
	height int
	ready  bool

	viewport viewport.Model
	entries  []StreamEvent

	connectedAt time.Time
	lastEventAt time.Time
	lastEventID int64
	totalEvents int
	missedGaps  int
	lastError   string
	streamEnded bool

	styles styles
}

type styles struct {
	title   lipgloss.Style
	muted   lipgloss.Style
	info    lipgloss.Style
	success lipgloss.Style
	warn    lipgloss.Style
	error   lipgloss.Style
	panel   lipgloss.Style
	header  lipgloss.Style
	footer  lipgloss.Style
}

// NewProgram builds the Bubble Tea program for the BeautyThing interface.
func NewProgram(ctx context.Context, cfg Config, stream <-chan StreamEvent, errs <-chan error) *tea.Program {
	msgs := make(chan tea.Msg)

	go func() {
		defer close(msgs)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				msgs <- streamErrMsg{err: err}
			case event, ok := <-stream:
				if !ok {
					msgs <- streamClosedMsg{}
					return
				}
				msgs <- event
			}
		}
	}()

	m := model{
		cfg:         cfg,
		msgs:        msgs,
		connectedAt: time.Now(),
		styles:      makeStyles(!cfg.NoColor),
	}

	if m.cfg.MaxEvents <= 0 {
		m.cfg.MaxEvents = 400
	}

	return tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
}

func makeStyles(color bool) styles {
	if !color {
		base := lipgloss.NewStyle()
		return styles{
			title:   base.Bold(true),
			muted:   base,
			info:    base,
			success: base,
			warn:    base,
			error:   base,
			panel:   base,
			header:  base.Bold(true),
			footer:  base,
		}
	}

	return styles{
		title:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		muted:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		info:    lipgloss.NewStyle().Foreground(lipgloss.Color("117")),
		success: lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		warn:    lipgloss.NewStyle().Foreground(lipgloss.Color("221")),
		error:   lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1),
		header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")),
		footer: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForMessage(m.msgs), tickCmd())
}

func waitForMessage(msgs <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if msgs == nil {
			return streamClosedMsg{}
		}
		msg, ok := <-msgs
		if !ok {
			return streamClosedMsg{}
		}
		return msg
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "g":
			m.viewport.GotoTop()
			return m, nil
		case "G":
			m.viewport.GotoBottom()
			return m, nil
		case "c":
			m.entries = nil
			m.totalEvents = 0
			m.missedGaps = 0
			m.lastEventAt = time.Time{}
			m.lastEventID = 0
			m.lastError = ""
			m.refreshViewport()
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil
	case StreamEvent:
		if msg.Gap {
			m.missedGaps++
		}
		m.entries = append(m.entries, msg)
		if len(m.entries) > m.cfg.MaxEvents {
			m.entries = append([]StreamEvent(nil), m.entries[len(m.entries)-m.cfg.MaxEvents:]...)
		}
		m.totalEvents++
		m.lastEventAt = msg.At
		m.lastEventID = msg.Entry.ID
		m.lastError = ""
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, waitForMessage(m.msgs)
	case streamErrMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
		return m, waitForMessage(m.msgs)
	case streamClosedMsg:
		m.streamEnded = true
		return m, nil
	case tickMsg:
		return m, tickCmd()
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *model) layout() {
	headerHeight := 6
	footerHeight := 2
	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	if !m.ready {
		m.viewport = viewport.New(max(20, m.width-2), bodyHeight)
		m.ready = true
	} else {
		m.viewport.Width = max(20, m.width-2)
		m.viewport.Height = bodyHeight
	}

	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) refreshViewport() {
	if !m.ready {
		return
	}

	lines := make([]string, 0, len(m.entries)+2)
	for _, entry := range m.entries {
		if entry.Gap {
			lines = append(lines, m.styles.warn.Render("warning  missed one or more events before #"+fmt.Sprint(entry.Entry.ID)))
		}
		lines = append(lines, m.renderFeedLine(entry))
	}

	if len(lines) == 0 {
		lines = append(lines, m.styles.muted.Render("waiting for Syncthing events..."))
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading BeautyThing..."
	}

	header := m.renderHeader()
	body := m.styles.panel.Width(max(20, m.width-2)).Render(m.viewport.View())
	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m model) renderHeader() string {
	mode := "all events"
	if m.cfg.DiskOnly {
		mode = "disk changes"
	}

	streamState := "live"
	streamTone := m.styles.success
	if m.streamEnded {
		streamState = "stopped"
		streamTone = m.styles.warn
	}
	if m.lastError != "" {
		streamState = "degraded"
		streamTone = m.styles.error
	}

	title := m.styles.title.Render("BeautyThing") + "  " + m.styles.muted.Render("realtime Syncthing activity")
	statusLine := strings.Join([]string{
		streamTone.Render(streamState),
		m.styles.muted.Render("source " + m.cfg.SourceURL),
		m.styles.muted.Render("mode " + mode),
		m.styles.muted.Render(fmt.Sprintf("start #%d", m.cfg.StartID)),
	}, "   ")

	eventLine := strings.Join([]string{
		m.styles.header.Render(fmt.Sprintf("%d events", m.totalEvents)),
		m.styles.muted.Render(fmt.Sprintf("last #%d", m.lastEventID)),
		m.styles.muted.Render("updated " + render.SinceLabel(time.Now(), m.lastEventAt)),
		m.styles.muted.Render(fmt.Sprintf("%d gaps", m.missedGaps)),
	}, "   ")

	errorLine := m.styles.muted.Render("status clean")
	if m.lastError != "" {
		errorLine = m.styles.error.Render("status " + m.lastError)
	}

	return m.styles.panel.Width(max(20, m.width-2)).Render(
		lipgloss.JoinVertical(lipgloss.Left, title, statusLine, eventLine, errorLine),
	)
}

func (m model) renderFooter() string {
	left := m.styles.footer.Render("j/k scroll  g/G top/bottom  c clear  q quit")
	right := m.styles.footer.Render(fmt.Sprintf("connected %s", render.SinceLabel(time.Now(), m.connectedAt)))

	width := max(20, m.width-2)
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > width {
		return m.styles.panel.Width(width).Render(left)
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	return m.styles.panel.Width(width).Render(left + strings.Repeat(" ", gap) + right)
}

func (m model) renderFeedLine(event StreamEvent) string {
	timeCol := m.styles.muted.Width(8).Render(event.Entry.Time)
	typeCol := m.styleForTone(event.Entry.Tone).Width(18).Render(truncate(event.Entry.Type, 18))
	idCol := m.styles.muted.Width(8).Render("#" + fmt.Sprint(event.Entry.ID))
	summaryWidth := max(20, m.viewport.Width-8-18-8-6)
	summary := truncate(event.Entry.Summary, summaryWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, timeCol, "  ", typeCol, "  ", idCol, "  ", summary)
}

func (m model) styleForTone(tone render.Tone) lipgloss.Style {
	switch tone {
	case render.ToneInfo:
		return m.styles.info
	case render.ToneSuccess:
		return m.styles.success
	case render.ToneWarn:
		return m.styles.warn
	case render.ToneError:
		return m.styles.error
	default:
		return m.styles.header
	}
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
