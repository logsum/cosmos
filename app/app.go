package app

import (
	"context"
	"cosmos/config"
	"cosmos/core"
	"cosmos/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// Application holds all wired dependencies and manages the application lifecycle.
type Application struct {
	Config            config.Config
	Session           *core.Session
	Scaffold          *ui.Scaffold
	Program           *tea.Program
	CurrencyFormatter *core.CurrencyFormatter
	Tracker           *core.Tracker
}

// Run starts the application and blocks until it exits.
// Returns an error if initialization or runtime fails.
func (a *Application) Run(ctx context.Context) error {
	// Derive a cancelable context so in-flight provider calls are interrupted on exit.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start core session
	a.Session.Start(ctx)
	defer a.Session.Stop()

	// Run Bubble Tea program (blocks until exit)
	if _, err := a.Program.Run(); err != nil {
		return err
	}

	return nil
}
