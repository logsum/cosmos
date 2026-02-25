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
//     which is goroutine-safe and unbounded. Falls back to the channel
//     if the program has not been set yet (pre-Run).
type Notifier struct {
	rcv       chan any
	listening bool
	mu        sync.Mutex
	program   *tea.Program
}

func newNotifier() *Notifier {
	return &Notifier{
		rcv: make(chan any, 256),
	}
}

// SetProgram stores a reference to the running tea.Program, enabling
// goroutine-safe message delivery via Send(). Call this once from main.go
// after tea.NewProgram() and before p.Run().
func (n *Notifier) SetProgram(p *tea.Program) {
	n.program = p
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
// If the tea.Program has been set (via SetProgram), it uses p.Send()
// which is goroutine-safe and unbounded. Otherwise, it falls back to
// the buffered channel and logs a warning if the message is dropped.
func (n *Notifier) Send(msg tea.Msg) {
	if n.program != nil {
		n.program.Send(msg)
		return
	}
	// Pre-Run() fallback: use the channel.
	select {
	case n.rcv <- msg:
	default:
		log.Printf("notifier: dropped message %T (program not yet set, channel full)", msg)
	}
}
