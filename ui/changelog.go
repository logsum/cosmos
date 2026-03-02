package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type changelogEntry struct {
	interactionID string
	timestamp     string
	description   string
	files         []ChangelogFile
	expanded      bool
}

// RestoreFunc triggers file restoration for an interaction.
// Returns a tea.Cmd that will produce a ChangelogRestoreResultMsg.
type RestoreFunc func(interactionID string) tea.Cmd

type ChangelogModel struct {
	entries        []changelogEntry
	cursor         int
	restoreFocused bool
	message        string
	scaffold       *Scaffold
	restoreFunc    RestoreFunc
}

func NewChangelogModel(scaffold *Scaffold, restoreFunc RestoreFunc) *ChangelogModel {
	return &ChangelogModel{
		scaffold:    scaffold,
		restoreFunc: restoreFunc,
	}
}

func (m *ChangelogModel) Init() tea.Cmd {
	return nil
}

func (m *ChangelogModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ChangelogEntryMsg:
		// Merge into existing entry with the same interactionID, or prepend new.
		found := false
		for i, e := range m.entries {
			if e.interactionID == msg.InteractionID {
				m.entries[i].files = append(m.entries[i].files, msg.Files...)
				m.entries[i].description = msg.Description
				found = true
				break
			}
		}
		if !found {
			entry := changelogEntry{
				interactionID: msg.InteractionID,
				timestamp:     msg.Timestamp,
				description:   msg.Description,
				files:         msg.Files,
			}
			// Prepend (newest first).
			m.entries = append([]changelogEntry{entry}, m.entries...)
			// Shift cursor down so the user's current selection stays on the
			// same entry after the new one is inserted above it. Without this,
			// each prepend would move the highlight to the newly arrived entry.
			if len(m.entries) > 1 {
				m.cursor++
			}
		}

	case ChangelogRestoreResultMsg:
		if msg.Success {
			m.message = "Restored: " + msg.Message
		} else {
			m.message = "Restore failed: " + msg.Message
		}

	case tea.KeyMsg:
		if len(m.entries) == 0 {
			return m, nil
		}
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
			if m.restoreFocused && m.restoreFunc != nil {
				m.message = ""
				entry := m.entries[m.cursor]
				m.entries[m.cursor].expanded = false
				m.restoreFocused = false
				return m, m.restoreFunc(entry.interactionID)
			} else if !m.restoreFocused {
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
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	pipe := pipeStyle.Render("│")

	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(headerStyle.Render("Project History"))
	b.WriteString("\n\n")

	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  No file changes recorded yet."))
		b.WriteString("\n")
		return b.String()
	}

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
			for _, f := range entry.files {
				shortPath := filepath.Base(f.Path)
				var detail string
				if f.Operation == "delete" {
					detail = "[" + redStyle.Render("delete") + "] " + dimStyle.Render(shortPath)
				} else if f.WasNew {
					detail = "[" + greenStyle.Render("new") + "] " + dimStyle.Render(shortPath)
				} else {
					detail = "[" + greenStyle.Render("write") + "] " + dimStyle.Render(shortPath)
				}
				b.WriteString("  " + pipe + "  " + detail + "\n")
			}
			b.WriteString("  " + pipe + "\n")

			btn := "[ Restore ]"
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
