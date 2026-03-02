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
	Placeholder        string
	CharLimit          int
	Width              int
	PromptGlyph        string
	CompletionProvider CompletionProvider // optional; enables tab-cycling completions
}

// App is a top-level tea.Model that wraps a Scaffold with a text-input prompt.
type App struct {
	Scaffold          *Scaffold
	promptInput       textinput.Model
	promptGlyph       string
	permissionPending bool // True when an inline permission prompt is active in chat

	completionProvider CompletionProvider
	completions        []string
	completionIdx      int // -1 = no active selection
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
		Scaffold:           scaffold,
		promptInput:        ti,
		promptGlyph:        glyph,
		completionProvider: cfg.CompletionProvider,
		completionIdx:      -1,
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.Scaffold.Init(), textinput.Blink)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	promptEnabled := a.isPromptEnabled()

	// Track permission state so we can route y/n keys correctly.
	switch msg.(type) {
	case ChatPermissionRequestMsg:
		a.permissionPending = true
	case PermissionDecisionMsg, ChatPermissionTimeoutMsg:
		a.permissionPending = false
	}

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
		// Tab-cycling completion (intercept before prompt input consumes tab).
		if promptEnabled && a.completionProvider != nil {
			switch msg.String() {
			case "tab":
				if len(a.completions) == 0 {
					a.completions = a.completionProvider.Completions(a.promptInput.Value())
					a.completionIdx = -1
				}
				if len(a.completions) > 0 {
					a.completionIdx = (a.completionIdx + 1) % len(a.completions)
					a.promptInput.SetValue(a.completions[a.completionIdx])
				}
				return a, nil // Don't forward tab to scaffold or prompt input

			case "shift+tab":
				if len(a.completions) > 0 {
					a.completionIdx--
					if a.completionIdx < 0 {
						a.completionIdx = len(a.completions) - 1
					}
					a.promptInput.SetValue(a.completions[a.completionIdx])
				}
				return a, nil
			}
		}

		// Any non-tab key clears the completion list.
		if msg.String() != "tab" && msg.String() != "shift+tab" {
			a.completions = nil
			a.completionIdx = -1
		}

		if promptEnabled && msg.String() == "enter" && a.promptInput.Value() != "" {
			value := a.promptInput.Value()
			a.promptInput.SetValue("")
			// Send PromptSubmitMsg through the scaffold so the active page receives it.
			updated, cmd := a.Scaffold.Update(PromptSubmitMsg{Value: value})
			a.Scaffold = updated.(*Scaffold)
			return a, cmd
		}

		// When a permission prompt is active, forward y/n directly to the chat page
		// (bypassing the text input which would consume them as typed characters).
		if a.permissionPending {
			switch msg.String() {
			case "y", "Y", "n", "N":
				updated, cmd := a.Scaffold.Update(msg)
				a.Scaffold = updated.(*Scaffold)
				return a, cmd
			}
		}
	}

	// Update prompt input only if enabled.
	if promptEnabled {
		var cmd tea.Cmd
		a.promptInput, cmd = a.promptInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Always forward to scaffold — it handles ctrl+c, tab switching (shift+arrows,
	// brackets), and routes messages to the active page. The prompt input and scaffold
	// both receive key messages; this is intentional (same behavior as pre-permission code).
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
