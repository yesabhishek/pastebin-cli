package editor

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Saver interface {
	Save(context.Context, string) (SaveResult, error)
	SaveRecovery(context.Context, string) error
	ClearRecovery() error
}

type SaveResult struct {
	Path         string
	ConflictPath string
	Message      string
	RemoteSaved  bool
}

type autosaveTick time.Time
type saveDoneMsg struct {
	result SaveResult
	err    error
}
type recoveryDoneMsg struct {
	err error
}

type Model struct {
	title     string
	path      string
	textarea  textarea.Model
	saver     Saver
	status    string
	lastSaved time.Time
	dirty     bool
	saving    bool
	quitting  bool
	width     int
	height    int
}

func New(title, path, initial string, saver Saver) Model {
	ta := textarea.New()
	ta.Placeholder = "Write your text here..."
	ta.SetValue(initial)
	ta.Focus()
	ta.ShowLineNumbers = true
	ta.CharLimit = 0
	ta.SetWidth(100)
	ta.SetHeight(24)
	return Model{
		title:    title,
		path:     path,
		textarea: ta,
		saver:    saver,
		status:   "Ctrl+S save • Ctrl+Q quit • autosave every 2s",
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tickAutosave())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(maxInt(20, msg.Width-4))
		m.textarea.SetHeight(maxInt(10, msg.Height-6))
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlS:
			if m.saving {
				return m, nil
			}
			m.saving = true
			m.status = "Saving..."
			return m, m.saveCmd()
		case tea.KeyCtrlQ:
			if m.saving {
				return m, nil
			}
			m.quitting = true
			if !m.dirty {
				if m.saver != nil {
					_ = m.saver.ClearRecovery()
				}
				return m, tea.Quit
			}
			m.saving = true
			m.status = "Saving before quit..."
			return m, m.saveCmd()
		}
		m.dirty = true
		m.status = "Unsaved changes"
		if m.saver != nil {
			return updateTextarea(&m, msg)
		}
	case autosaveTick:
		cmds := []tea.Cmd{tickAutosave()}
		if m.dirty && !m.saving {
			m.saving = true
			m.status = "Autosaving..."
			cmds = append(cmds, m.saveCmd())
		} else if m.dirty && m.saver != nil {
			cmds = append(cmds, m.recoveryCmd())
		}
		return m, tea.Batch(cmds...)
	case saveDoneMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "Save failed: " + msg.err.Error()
			m.dirty = true
			m.quitting = false
		} else {
			m.lastSaved = time.Now()
			m.dirty = false
			m.path = msg.result.Path
			if msg.result.ConflictPath != "" {
				m.path = msg.result.ConflictPath
			}
			if msg.result.Message != "" {
				m.status = msg.result.Message
			} else {
				m.status = "Saved"
			}
		}
		if m.quitting {
			if m.saver != nil {
				_ = m.saver.ClearRecovery()
			}
			return m, tea.Quit
		}
	case recoveryDoneMsg:
		if msg.err != nil {
			m.status = "Autosave recovery failed: " + msg.err.Error()
		}
	}
	return updateTextarea(&m, msg)
}

func (m Model) View() string {
	header := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%s  [%s]", m.title, m.path))
	status := m.status
	if !m.lastSaved.IsZero() {
		status = fmt.Sprintf("%s • last save %s", status, m.lastSaved.Format("15:04:05"))
	}
	footer := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(status)
	return strings.Join([]string{header, "", m.textarea.View(), "", footer}, "\n")
}

func Run(in io.Reader, out io.Writer, model Model) error {
	p := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) saveCmd() tea.Cmd {
	content := m.textarea.Value()
	return func() tea.Msg {
		result, err := m.saver.Save(context.Background(), content)
		return saveDoneMsg{result: result, err: err}
	}
}

func (m Model) recoveryCmd() tea.Cmd {
	content := m.textarea.Value()
	return func() tea.Msg {
		err := m.saver.SaveRecovery(context.Background(), content)
		return recoveryDoneMsg{err: err}
	}
}

func tickAutosave() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return autosaveTick(t)
	})
}

func updateTextarea(m *Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return *m, cmd
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
