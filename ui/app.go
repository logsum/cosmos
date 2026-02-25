package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// PromptSubmitMsg is sent when the user presses Enter with non-empty input.
// Page models handle this in their own Update method.
type PromptSubmitMsg struct {
	Value string
}

// AppConfig holds optional configuration for an App.
type AppConfig struct {
	Placeholder string
	CharLimit   int
	Width       int
	PromptGlyph string
}

// App is a top-level tea.Model that wraps a Scaffold with a text-input prompt.
type App struct {
	Scaffold    *Scaffold
	promptInput textinput.Model
	promptGlyph string
}

// NewApp creates an App from an existing Scaffold and config.
func NewApp(scaffold *Scaffold, cfg AppConfig) *App {
	ti := textinput.New()
	ti.Prompt = "" // We render our own glyph prefix.
	ti.Focus()

	if cfg.Placeholder != "" {
		ti.Placeholder = cfg.Placeholder
	}
	if cfg.CharLimit > 0 {
		ti.CharLimit = cfg.CharLimit
	}
	if cfg.Width > 0 {
		ti.Width = cfg.Width
	} else {
		ti.Width = 80
	}

	glyph := "❯"
	if cfg.PromptGlyph != "" {
		glyph = cfg.PromptGlyph
	}

	return &App{
		Scaffold:    scaffold,
		promptInput: ti,
		promptGlyph: glyph,
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.Scaffold.Init(), textinput.Blink)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	promptEnabled := a.isPromptEnabled()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.promptInput.Width = msg.Width - 4

		heightAdjustment := 0
		if promptEnabled {
			heightAdjustment = 1
		}

		modifiedMsg := tea.WindowSizeMsg{
			Width:  msg.Width,
			Height: msg.Height - heightAdjustment,
		}

		updated, cmd := a.Scaffold.Update(modifiedMsg)
		a.Scaffold = updated.(*Scaffold)
		return a, cmd

	case tea.KeyMsg:
		if promptEnabled && msg.String() == "enter" && a.promptInput.Value() != "" {
			value := a.promptInput.Value()
			a.promptInput.SetValue("")
			// Send PromptSubmitMsg through the scaffold so the active page receives it.
			updated, cmd := a.Scaffold.Update(PromptSubmitMsg{Value: value})
			a.Scaffold = updated.(*Scaffold)
			return a, cmd
		}
	}

	// Update prompt input only if enabled.
	if promptEnabled {
		var cmd tea.Cmd
		a.promptInput, cmd = a.promptInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Update scaffold.
	updated, scaffoldCmd := a.Scaffold.Update(msg)
	a.Scaffold = updated.(*Scaffold)
	cmds = append(cmds, scaffoldCmd)

	return a, tea.Batch(cmds...)
}

// isPromptEnabled returns whether the prompt should be shown for the current page.
func (a *App) isPromptEnabled() bool {
	currentPage := a.Scaffold.GetCurrentPageKey()
	// Disable prompt for changelog and agents pages
	return currentPage != "changelog" && currentPage != "agents"
}

func (a *App) View() string {
	scaffoldView := a.Scaffold.View()
	if a.isPromptEnabled() {
		return scaffoldView + "\n" + a.promptGlyph + " " + a.promptInput.View()
	}
	return scaffoldView
}
