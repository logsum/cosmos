package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type InfoPage struct {
	scaffold *Scaffold
	title    string
}

func NewInfoPage(scaffold *Scaffold, title string) *InfoPage {
	return &InfoPage{
		scaffold: scaffold,
		title:    title,
	}
}

func (m InfoPage) Init() tea.Cmd {
	return nil
}

func (m InfoPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m InfoPage) View() string {
	verticalCenter := m.scaffold.GetTerminalHeight()/2 - 3
	if verticalCenter < 0 {
		verticalCenter = 0
	}
	requiredNewLines := strings.Repeat("\n", verticalCenter)

	content := fmt.Sprintf("%s | %d x %d\n\n", m.title, m.scaffold.GetTerminalWidth(), m.scaffold.GetTerminalHeight())
	content += "Controls:\n"
	content += "  Shift+Left/Right or [ / ] - Switch tabs\n"
	content += "  Ctrl+C - Exit"

	return requiredNewLines + content
}
