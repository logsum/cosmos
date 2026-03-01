package app

import (
	"context"
	"cosmos/config"
	"cosmos/core"
	"cosmos/engine/runtime"
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
	Executor          *runtime.V8Executor // V8 isolates; Close() on exit
}

// Run starts the application and blocks until it exits.
// Returns an error if initialization or runtime fails.
func (a *Application) Run(ctx context.Context) error {
	// Derive a cancelable context so in-flight provider calls are interrupted on exit.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Dispose V8 isolates on exit.
	// Deferred before Session.Stop() so LIFO ordering runs: Stop() first, then Close().
	// This ensures no tool execution is in-flight when isolates are torn down.
	if a.Executor != nil {
		defer a.Executor.Close()
	}

	// Start core session
	a.Session.Start(ctx)
	defer a.Session.Stop()

	// Run Bubble Tea program (blocks until exit)
	if _, err := a.Program.Run(); err != nil {
		return err
	}

	return nil
}
