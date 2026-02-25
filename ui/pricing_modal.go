package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type PricingDetail struct {
	ModelName    string
	InputTokens  int64
	OutputTokens int64
	InputCost    float64
	OutputCost   float64
	TotalCost    float64
}

type PricingModal struct {
	visible bool
	width   int
	height  int
	details PricingDetail
}

func NewPricingModal() *PricingModal {
	// Mock data for initial implementation
	return &PricingModal{
		visible: false,
		details: PricingDetail{
			ModelName:    "claude-opus-4-6",
			InputTokens:  15420,
			OutputTokens: 8305,
			InputCost:    0.46,
			OutputCost:   0.27,
			TotalCost:    0.73,
		},
	}
}

func (pm *PricingModal) Show() {
	pm.visible = true
}

func (pm *PricingModal) Hide() {
	pm.visible = false
}

func (pm *PricingModal) IsVisible() bool {
	return pm.visible
}

func (pm *PricingModal) SetSize(width, height int) {
	pm.width = width
	pm.height = height
}

func (pm *PricingModal) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		pm.SetSize(msg.Width, msg.Height)
	}
	return nil
}

func (pm *PricingModal) View() string {
	if !pm.visible {
		return ""
	}

	orangeColor := lipgloss.Color("208")
	grayColor := lipgloss.Color("245")

	titleStyle := lipgloss.NewStyle().
		Foreground(orangeColor).
		Bold(true).
		MarginBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(grayColor)

	valueStyle := lipgloss.NewStyle().
		Bold(true)

	totalStyle := lipgloss.NewStyle().
		Foreground(orangeColor).
		Bold(true)

	dividerStyle := lipgloss.NewStyle().
		Foreground(grayColor)

	helpStyle := lipgloss.NewStyle().
		Foreground(grayColor).
		Italic(true).
		MarginTop(1)

	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("💰 Cost Breakdown"))
	b.WriteString("\n\n")

	// Model name
	b.WriteString(labelStyle.Render("Model: "))
	b.WriteString(valueStyle.Render(pm.details.ModelName))
	b.WriteString("\n\n")

	// Divider
	b.WriteString(dividerStyle.Render(strings.Repeat("─", 56)))
	b.WriteString("\n\n")

	// Input tokens
	b.WriteString(labelStyle.Render("Input tokens:  "))
	b.WriteString(valueStyle.Render(fmt.Sprintf("%d", pm.details.InputTokens)))
	b.WriteString(labelStyle.Render("  →  "))
	b.WriteString(valueStyle.Render(fmt.Sprintf("$%.4f", pm.details.InputCost)))
	b.WriteString("\n")

	// Output tokens
	b.WriteString(labelStyle.Render("Output tokens: "))
	b.WriteString(valueStyle.Render(fmt.Sprintf("%d", pm.details.OutputTokens)))
	b.WriteString(labelStyle.Render("  →  "))
	b.WriteString(valueStyle.Render(fmt.Sprintf("$%.4f", pm.details.OutputCost)))
	b.WriteString("\n\n")

	// Divider
	b.WriteString(dividerStyle.Render(strings.Repeat("─", 56)))
	b.WriteString("\n\n")

	// Total cost
	b.WriteString(labelStyle.Render("Total: "))
	b.WriteString(totalStyle.Render(fmt.Sprintf("$%.2f", pm.details.TotalCost)))
	b.WriteString("\n")

	// Help text
	b.WriteString(helpStyle.Render("Press Esc or Enter to close"))

	content := b.String()

	// Box with rounded border
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(orangeColor).
		Padding(1, 2).
		Width(60)

	boxed := boxStyle.Render(content)

	// Center the modal
	return lipgloss.Place(
		pm.width,
		pm.height,
		lipgloss.Center,
		lipgloss.Center,
		boxed,
	)
}
