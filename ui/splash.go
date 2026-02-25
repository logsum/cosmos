package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Splash renders the welcome screen shown when chat is empty.
type Splash struct{}

func NewSplash() *Splash {
	return &Splash{}
}

func (s *Splash) View() string {
	sprite := []string{
		`          _.-._          `,
		"       .-'  _  `-.       ",
		`     .'   .' '.   '.     `,
		`    /    /  _  \    \    `,
		`   ;    |  (_)  |    ;   `,
		`   |    |       |    |   `,
		`   ;    |_     _|    ;   `,
		`    \    \.   ./    /    `,
		`     '.  (_____)  .'     `,
		`       '-._\_/_..-'      `,
		`           /_\           `,
		`          /___\          `,
	}

	help := []string{
		"Welcome to Cosmos Code",
		"",
		"Shortcuts",
		"",
		"  Shift+Left/Right or [ / ]  Switch tabs",
		"  Enter                      Send message",
		"  Ctrl+C                     Exit",
	}
	helpStart := 5

	maxSpriteWidth := 0
	for _, row := range sprite {
		if w := lipgloss.Width(row); w > maxSpriteWidth {
			maxSpriteWidth = w
		}
	}

	alienStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	var b strings.Builder
	for y, row := range sprite {
		b.WriteString(alienStyle.Render(row))
		if pad := maxSpriteWidth - lipgloss.Width(row); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}

		helpIdx := y - helpStart
		if helpIdx >= 0 && helpIdx < len(help) {
			b.WriteString("   ")
			b.WriteString(help[helpIdx])
		}
		b.WriteString("\n")
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("135")).
		Padding(0, 1, 1, 1)

	return box.Render(strings.TrimRight(b.String(), "\n"))
}
