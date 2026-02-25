package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tab struct {
	key   string
	title string
}

type tabBar struct {
	termReady  bool
	currentTab int
	width      int
	tabs       []tab

	borderColor         string
	activeBorderColor   string
	inactiveBorderColor string
	leftPadding         int
	rightPadding        int
	titleStyleActive    lipgloss.Style
	titleStyleInactive  lipgloss.Style

	titleLength int
}

func newTabBar() *tabBar {
	borderColor := "39"
	lp := 1
	rp := 1
	return &tabBar{
		borderColor:         borderColor,
		activeBorderColor:   "205",
		inactiveBorderColor: "255",
		leftPadding:         lp,
		rightPadding:        rp,
		titleStyleActive:    lipgloss.NewStyle().PaddingLeft(lp).PaddingRight(rp),
		titleStyleInactive:  lipgloss.NewStyle().PaddingLeft(lp).PaddingRight(rp).Foreground(lipgloss.Color("245")),
	}
}

// TabBarSizeMsg is sent when the terminal width changes to report
// whether there is enough room for the tab bar.
type TabBarSizeMsg struct {
	NotEnoughToHandleTabs bool
}

func (tb *tabBar) addTab(key, title string) {
	tb.tabs = append(tb.tabs, tab{key: key, title: title})
	tb.recalc()
}

func (tb *tabBar) recalc() tea.Cmd {
	var totalLen int
	for _, t := range tb.tabs {
		totalLen += len([]rune(t.title))
		totalLen += tb.leftPadding + tb.rightPadding
		totalLen += 2
	}
	tb.titleLength = totalLen + 2 // +2 for "⌬ " icon on active tab

	remaining := tb.width - (tb.titleLength + 2)
	if remaining < 0 {
		return func() tea.Msg { return TabBarSizeMsg{NotEnoughToHandleTabs: true} }
	}
	return func() tea.Msg { return TabBarSizeMsg{NotEnoughToHandleTabs: false} }
}

func (tb *tabBar) SetBorderColor(color string) {
	tb.borderColor = color
}

func (tb *tabBar) SetActiveTabBorderColor(color string) {
	tb.activeBorderColor = color
}

func (tb *tabBar) SetInactiveTabBorderColor(color string) {
	tb.inactiveBorderColor = color
}

func (tb *tabBar) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !tb.termReady && msg.Width > 0 && msg.Height > 0 {
			tb.termReady = true
		}
		tb.width = msg.Width
		return tb.recalc()
	}
	return nil
}

func (tb *tabBar) View() string {
	if !tb.termReady {
		return "setting up terminal..."
	}

	tabs, tabsLen := tb.renderTabs()
	remaining := tb.width - (tabsLen + 2)
	if remaining < 0 {
		return ""
	}

	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(tb.borderColor))

	var b strings.Builder
	b.WriteString(borderStyle.Render("╭"))
	b.WriteString(tabs)
	b.WriteString(borderStyle.Render(strings.Repeat("─", remaining)))
	b.WriteString(borderStyle.Render("╮"))

	return b.String()
}

func (tb *tabBar) renderTabs() (string, int) {
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(tb.activeBorderColor))
	inactiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(tb.inactiveBorderColor))

	var b strings.Builder
	for i, t := range tb.tabs {
		if i == tb.currentTab {
			b.WriteString(activeStyle.Render("┤"))
			b.WriteString(tb.titleStyleActive.Foreground(lipgloss.Color(tb.activeBorderColor)).Render("⌬ " + t.title))
			b.WriteString(activeStyle.Render("├"))
		} else {
			b.WriteString(inactiveStyle.Render("┤"))
			b.WriteString(tb.titleStyleInactive.Render(t.title))
			b.WriteString(inactiveStyle.Render("├"))
		}
	}

	return b.String(), tb.titleLength
}
