package app

import (
	"context"
	"cosmos/config"
	"cosmos/core"
	"cosmos/engine/runtime"
	"cosmos/ui"
	"fmt"
	"os"

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

	// Dispose V8 isolates on exit (after session stops, so no tool is in-flight).
	if a.Executor != nil {
		defer a.Executor.Close()
	}

	// Start core session
	a.Session.Start(ctx)

	// Run Bubble Tea program (blocks until exit)
	_, runErr := a.Program.Run()

	// Stop the session loop first — guarantees the loop goroutine has fully
	// drained and no concurrent history mutations are in progress.
	cancel()
	a.Session.Stop()

	// Now it's safe to snapshot and persist the session.
	workDir, _ := os.Getwd()
	if err := core.SaveSession(a.Session, a.Tracker, a.Config.SessionsDir, workDir); err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: warning: session save failed: %v\n", err)
	}

	return runErr
}
