package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type changelogEntry struct {
	timestamp   string
	description string
	details     string
	expanded    bool
}

type ChangelogModel struct {
	entries        []changelogEntry
	cursor         int
	restoreFocused bool
	message        string
	scaffold       *Scaffold
}

func NewChangelogModel(scaffold *Scaffold) *ChangelogModel {
	return &ChangelogModel{
		scaffold: scaffold,
		entries: []changelogEntry{
			{
				timestamp:   "2026-02-21 15:30:00",
				description: "Extract App-builder logic into ui/ package",
				details:     "Moved scaffold, tab bar, status bar, and prompt into\nreusable ui/ package with clean public API.\nFiles: ui/scaffold.go, ui/app.go, ui/tabbar.go",
			},
			{
				timestamp:   "2026-02-21 14:00:00",
				description: "Add alien sprite to welcome screen",
				details:     "Ported Python sprite renderer to Go with true-color ANSI.\nHelp text displays beside the alien.\nFiles: main.go",
			},
			{
				timestamp:   "2026-02-21 12:45:00",
				description: "Customize keybindings for macOS",
				details:     "Replaced ctrl-based tab switching with shift+arrow keys\nand bracket shortcuts for macOS compatibility.\nFiles: main.go",
			},
			{
				timestamp:   "2026-02-21 11:20:00",
				description: "Add status bar with project info",
				details:     "Added five status items: directory, branch, model,\ntoken counts, and estimated cost.\nFiles: main.go",
			},
			{
				timestamp:   "2026-02-21 10:00:00",
				description: "Initial project setup with scaffold",
				details:     "Created Go module, added bubbletea scaffold with\nthree tabs (Chat, Agents, Changelog) and prompt input.\nFiles: main.go, go.mod, go.sum",
			},
		},
	}
}

func (m *ChangelogModel) Init() tea.Cmd {
	return nil
}

func (m *ChangelogModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			if m.restoreFocused {
				m.restoreFocused = false
			} else if m.cursor > 0 {
				m.cursor--
				if m.entries[m.cursor].expanded {
					m.restoreFocused = true
				}
			}
		case "down":
			if m.entries[m.cursor].expanded && !m.restoreFocused {
				m.restoreFocused = true
			} else {
				m.restoreFocused = false
				if m.cursor < len(m.entries)-1 {
					m.cursor++
				}
			}
		case "enter":
			if m.restoreFocused {
				m.message = "✓ Restored to " + m.entries[m.cursor].timestamp
				m.entries[m.cursor].expanded = false
				m.restoreFocused = false
			} else {
				m.entries[m.cursor].expanded = !m.entries[m.cursor].expanded
			}
		}
	}
	return m, nil
}

func (m *ChangelogModel) View() string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("93"))
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	pipeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	restoreNormal := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	restoreActive := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208")).Background(lipgloss.Color("235"))

	pipe := pipeStyle.Render("│")

	var b strings.Builder

	// Add header
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("Project History"))
	b.WriteString("\n\n")

	for i, entry := range m.entries {
		isCursor := i == m.cursor
		onHeader := isCursor && !m.restoreFocused
		arrow := "▸"
		if entry.expanded {
			arrow = "▾"
		}

		prefix := "  "
		if onHeader {
			prefix = "> "
		}

		line := fmt.Sprintf("%s  %s  %s", arrow, entry.timestamp, entry.description)
		if onHeader {
			b.WriteString(selectedStyle.Render(prefix + line))
		} else {
			b.WriteString(dimStyle.Render(prefix + line))
		}
		b.WriteString("\n")

		if entry.expanded {
			for _, dl := range strings.Split(entry.details, "\n") {
				b.WriteString("  " + pipe + "  " + dimStyle.Render(dl) + "\n")
			}
			b.WriteString("  " + pipe + "\n")

			btn := "[ ⟲ Restore ]"
			onRestore := isCursor && m.restoreFocused
			if onRestore {
				b.WriteString("  " + pipe + "  " + restoreActive.Render("> "+btn) + "\n")
			} else {
				b.WriteString("  " + pipe + "  " + restoreNormal.Render("  "+btn) + "\n")
			}
			b.WriteString("  " + pipe + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑↓ navigate   Enter expand/collapse/restore"))
	b.WriteString("\n")

	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(selectedStyle.Render("  " + m.message))
		b.WriteString("\n")
	}

	return b.String()
}
