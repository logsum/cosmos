package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// SessionSubmitter interface for submitting messages to the core session.
// This is defined here for use in AddDefaultPages but implemented by core.Session.
type SessionSubmitter interface {
	SubmitMessage(text string)
}

// CompletionProvider provides tab completion strings for a given input prefix.
// core.Session satisfies this interface without requiring a ui→core import.
type CompletionProvider interface {
	Completions(prefix string) []string
}

func ConfigureDefaultScaffold(s *Scaffold, currentDir string, model string) {
	s.KeyMap.SwitchTabLeft = key.NewBinding(
		key.WithKeys("shift+left", "["),
		key.WithHelp("shift+←/[", "previous tab"),
	)
	s.KeyMap.SwitchTabRight = key.NewBinding(
		key.WithKeys("shift+right", "]"),
		key.WithHelp("shift+→/]", "next tab"),
	)

	s.SetPagePosition(lipgloss.Left)
	s.SetStatusItemLeftPadding(1)
	s.SetStatusItemRightPadding(1)

	orangeColor := "208"
	s.SetBorderColor(orangeColor)
	s.SetActiveTabBorderColor(orangeColor)
	s.SetInactiveTabBorderColor(orangeColor)
	s.SetStatusItemBorderColor(orangeColor)

	s.AddStatusItem("path", "□ "+currentDir)
	s.AddStatusItem("branch", "⎇ main")
	s.AddStatusItem("model", "⚙ "+FormatModelName(model))
	s.AddStatusItem("tokens", "▲0 ▼0")
	s.AddStatusItem("context", "⚡0%")
	s.AddActionableStatusItem("cost", "$0.00")
}

// FormatModelName extracts a human-readable name from a full model ID.
// e.g. "us.anthropic.claude-3-5-sonnet-20241022-v2:0" → "claude-3-5-sonnet-20241022-v2"
func FormatModelName(modelID string) string {
	// Strip regional prefix (e.g., "us.", "eu.", "ap.")
	for _, prefix := range []string{"us.", "eu.", "ap."} {
		modelID = strings.TrimPrefix(modelID, prefix)
	}
	// Strip provider prefix (e.g., "anthropic.")
	if i := strings.Index(modelID, "."); i >= 0 {
		modelID = modelID[i+1:]
	}
	// Strip version suffix (e.g., ":0")
	if i := strings.LastIndex(modelID, ":"); i >= 0 {
		modelID = modelID[:i]
	}
	return modelID
}

func AddDefaultPages(s *Scaffold, session SessionSubmitter, tools []Tool) {
	s.AddPage("chat", "Chat", NewChatModel(session))
	s.AddPage("agents", "Agents", NewAgentsModel(s, tools))
	s.AddPage("changelog", "Changelog", NewChangelogModel(s))
}
