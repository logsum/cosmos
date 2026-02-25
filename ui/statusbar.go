package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type StatusItem struct {
	Key        string
	Value      string
	Actionable bool // Whether this item has a drill-down action (modal, picker, etc.)
}

type statusBar struct {
	termReady bool
	width     int
	items     []*StatusItem

	borderColor     string
	itemBorderColor string
	leftPadding     int
	rightPadding    int
	itemStyle       lipgloss.Style

	itemsLength int

	selectedIndex int  // -1 = none, 0..n = item index
	hasFocus      bool // Whether status bar has focus
}

func newStatusBar() *statusBar {
	borderColor := "39"
	lp := 2
	rp := 2
	return &statusBar{
		borderColor:     borderColor,
		itemBorderColor: "49",
		leftPadding:     lp,
		rightPadding:    rp,
		itemStyle:       lipgloss.NewStyle().PaddingLeft(lp).PaddingRight(rp),
		selectedIndex:   -1,
	}
}

// StatusBarSizeMsg is sent when the terminal width changes to report
// whether there is enough room for the status bar.
type StatusBarSizeMsg struct {
	NotEnoughToHandleStatusBar bool
}

func (sb *statusBar) addItem(key, value string, actionable bool) {
	for _, item := range sb.items {
		if item.Key == key {
			return
		}
	}
	sb.items = append(sb.items, &StatusItem{Key: key, Value: value, Actionable: actionable})
	sb.recalc()
}

func (sb *statusBar) recalc() tea.Cmd {
	var totalLen int
	for _, item := range sb.items {
		totalLen += len([]rune(item.Value))
		totalLen += sb.leftPadding + sb.rightPadding
	}
	// Add border characters: opening ┤ + closing ├ = 2
	if len(sb.items) > 0 {
		totalLen += 2
	}
	sb.itemsLength = totalLen

	remaining := sb.width - (totalLen + 2)
	if remaining < 0 {
		return func() tea.Msg { return StatusBarSizeMsg{NotEnoughToHandleStatusBar: true} }
	}
	return func() tea.Msg { return StatusBarSizeMsg{NotEnoughToHandleStatusBar: false} }
}

func (sb *statusBar) SetBorderColor(color string) {
	sb.borderColor = color
}

func (sb *statusBar) SetItemBorderColor(color string) {
	sb.itemBorderColor = color
}

func (sb *statusBar) SetLeftPadding(padding int) {
	sb.leftPadding = padding
	sb.itemStyle = sb.itemStyle.PaddingLeft(padding)
}

func (sb *statusBar) SetRightPadding(padding int) {
	sb.rightPadding = padding
	sb.itemStyle = sb.itemStyle.PaddingRight(padding)
}

func (sb *statusBar) SetFocus(focused bool) {
	sb.hasFocus = focused
	if !focused {
		sb.selectedIndex = -1
	} else {
		// Select first actionable item
		sb.selectedIndex = sb.findNextActionable(-1)
	}
}

func (sb *statusBar) SelectNext() {
	if len(sb.items) == 0 {
		return
	}
	sb.selectedIndex = sb.findNextActionable(sb.selectedIndex)
}

func (sb *statusBar) SelectPrev() {
	if len(sb.items) == 0 {
		return
	}
	sb.selectedIndex = sb.findPrevActionable(sb.selectedIndex)
}

func (sb *statusBar) HasActionableItems() bool {
	for _, item := range sb.items {
		if item.Actionable {
			return true
		}
	}
	return false
}

func (sb *statusBar) findNextActionable(fromIndex int) int {
	if len(sb.items) == 0 {
		return -1
	}
	start := fromIndex + 1
	for i := 0; i < len(sb.items); i++ {
		idx := (start + i) % len(sb.items)
		if sb.items[idx].Actionable {
			return idx
		}
	}
	return -1
}

func (sb *statusBar) findPrevActionable(fromIndex int) int {
	if len(sb.items) == 0 {
		return -1
	}
	start := fromIndex - 1
	if start < 0 {
		start = len(sb.items) - 1
	}
	for i := 0; i < len(sb.items); i++ {
		idx := start - i
		if idx < 0 {
			idx = len(sb.items) + idx
		}
		if sb.items[idx].Actionable {
			return idx
		}
	}
	return -1
}

func (sb *statusBar) GetSelectedItem() *StatusItem {
	if sb.selectedIndex >= 0 && sb.selectedIndex < len(sb.items) {
		return sb.items[sb.selectedIndex]
	}
	return nil
}

func (sb *statusBar) IsAtFirstActionable() bool {
	if sb.selectedIndex < 0 {
		return false
	}
	// Check if this is the first actionable item
	firstActionable := sb.findNextActionable(-1)
	return sb.selectedIndex == firstActionable
}

func (sb *statusBar) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !sb.termReady && msg.Width > 0 && msg.Height > 0 {
			sb.termReady = true
		}
		sb.width = msg.Width
		return sb.recalc()
	}
	return nil
}

func (sb *statusBar) View() string {
	if !sb.termReady {
		return "setting up terminal..."
	}

	items, itemsLen := sb.renderItems()
	remaining := sb.width - (itemsLen + 2)
	if remaining < 0 {
		return ""
	}

	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(sb.borderColor))

	var b strings.Builder
	b.WriteString(borderStyle.Render("╭"))
	b.WriteString(borderStyle.Render(strings.Repeat("─", remaining)))
	b.WriteString(items)
	b.WriteString(borderStyle.Render("╮"))

	return b.String()
}

func (sb *statusBar) renderItems() (string, int) {
	ibStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(sb.itemBorderColor))
	selectedItemStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("208")).
		Background(lipgloss.Color("235")).
		Bold(true).
		PaddingLeft(sb.leftPadding).
		PaddingRight(sb.rightPadding)

	var b strings.Builder
	// Opening bracket
	b.WriteString(ibStyle.Render("┤"))

	for i, item := range sb.items {
		// Render item (no separator between items)
		if sb.hasFocus && i == sb.selectedIndex {
			b.WriteString(selectedItemStyle.Render(item.Value))
		} else {
			b.WriteString(sb.itemStyle.Render(item.Value))
		}
	}

	// Closing bracket
	b.WriteString(ibStyle.Render("├"))

	return b.String(), sb.itemsLength
}
