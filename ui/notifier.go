package ui

import (
	"log"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// UpdateMsg signals that the scaffold should re-render.
type UpdateMsg struct{}

// Notifier provides a channel-based mechanism to trigger UI updates
// from outside the Bubble Tea update loop.
//
// It supports two modes of delivering messages:
//   - Notify(): sends an idempotent re-render signal via a buffered channel.
//     Drops are harmless since the next render will pick up current state.
//   - Send(msg): delivers a data-carrying message via tea.Program.Send(),
//     which is goroutine-safe and unbounded. Blocks until SetProgram() has
//     been called (prevents silent drops during initialization).
type Notifier struct {
	rcv       chan any
	listening bool
	mu        sync.Mutex
	program   *tea.Program
	initWg    sync.WaitGroup // blocks Send() until SetProgram() is called
}

func newNotifier() *Notifier {
	n := &Notifier{
		rcv: make(chan any, 256),
	}
	n.initWg.Add(1)
	return n
}

// SetProgram stores a reference to the running tea.Program, enabling
// goroutine-safe message delivery via Send(). Call this once from main.go
// after tea.NewProgram() and before p.Run().
func (n *Notifier) SetProgram(p *tea.Program) {
	n.program = p
	n.initWg.Done()
}

// Listen returns a tea.Cmd that blocks until a notification is sent.
func (n *Notifier) Listen() tea.Cmd {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.listening {
		return nil
	}
	n.listening = true

	return func() tea.Msg {
		msg := <-n.rcv
		n.mu.Lock()
		n.listening = false
		n.mu.Unlock()
		return msg
	}
}

// Notify sends an UpdateMsg through the channel.
func (n *Notifier) Notify() {
	select {
	case n.rcv <- UpdateMsg{}:
	default:
	}
}

// Send delivers a data-carrying message to the Bubble Tea runtime.
// Blocks until SetProgram() has been called to ensure no messages are
// silently dropped during initialization.
func (n *Notifier) Send(msg tea.Msg) {
	n.initWg.Wait()
	if n.program != nil {
		n.program.Send(msg)
		return
	}
	// Defensive fallback (should not happen after SetProgram).
	select {
	case n.rcv <- msg:
	default:
		log.Printf("notifier: dropped message %T (channel full)", msg)
	}
}
