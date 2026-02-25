package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const mergedHeaderHeight = 1

// StatusItemUpdateMsg is a goroutine-safe message for updating a status bar
// item. Send it via Notifier.Send() from any goroutine. The mutation is
// applied inside Scaffold.Update() on the Bubble Tea goroutine.
type StatusItemUpdateMsg struct {
	Key   string
	Value string
}

// Scaffold manages a tabbed terminal UI with a tab bar, a body
// (page content), and a status bar.
type Scaffold struct {
	termReady                          bool
	termSizeNotEnoughToHandleTabs      bool
	termSizeNotEnoughToHandleStatusBar bool

	currentTab int
	width      int
	height     int

	tabBar    *tabBar
	statusBar *statusBar
	KeyMap    *KeyMap
	pages     []tea.Model

	borderColor  string
	pagePosition lipgloss.Position

	notifier *Notifier

	statusBarFocusMode bool
	pricingModal       *PricingModal
}

// NewScaffold returns a new Scaffold with sensible defaults.
func NewScaffold() *Scaffold {
	return &Scaffold{
		borderColor:  "39",
		pagePosition: lipgloss.Center,
		width:        80,
		height:       24,
		tabBar:       newTabBar(),
		statusBar:    newStatusBar(),
		KeyMap:       newKeyMap(),
		notifier:     newNotifier(),
		pricingModal: NewPricingModal(),
	}
}

// GetNotifier returns the scaffold's Notifier, allowing external code
// (e.g., core/) to send goroutine-safe messages via Send().
func (s *Scaffold) GetNotifier() *Notifier {
	return s.notifier
}

// --- Configuration methods (chainable, setup-only) ---
//
// These methods mutate Scaffold fields directly and are NOT goroutine-safe.
// Call them only during setup, before tea.Program.Run().
// For runtime updates from goroutines, use Notifier.Send() with typed messages
// (e.g., StatusItemUpdateMsg).

// SetBorderColor sets the border color on tab bar, status bar, and body.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetBorderColor(color string) *Scaffold {
	s.tabBar.SetBorderColor(color)
	s.statusBar.SetBorderColor(color)
	s.borderColor = color
	s.notifier.Notify()
	return s
}

// SetPagePosition sets the horizontal alignment of page content.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetPagePosition(position lipgloss.Position) *Scaffold {
	s.pagePosition = position
	s.notifier.Notify()
	return s
}

// SetActiveTabBorderColor sets the border color on the active tab.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetActiveTabBorderColor(color string) *Scaffold {
	s.tabBar.SetActiveTabBorderColor(color)
	s.notifier.Notify()
	return s
}

// SetInactiveTabBorderColor sets the border color on inactive tabs.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetInactiveTabBorderColor(color string) *Scaffold {
	s.tabBar.SetInactiveTabBorderColor(color)
	s.notifier.Notify()
	return s
}

// SetStatusItemBorderColor sets the border color on status bar items.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetStatusItemBorderColor(color string) *Scaffold {
	s.statusBar.SetItemBorderColor(color)
	s.notifier.Notify()
	return s
}

// SetStatusItemLeftPadding sets the left padding inside each status item.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetStatusItemLeftPadding(padding int) *Scaffold {
	s.statusBar.SetLeftPadding(padding)
	s.notifier.Notify()
	return s
}

// SetStatusItemRightPadding sets the right padding inside each status item.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) SetStatusItemRightPadding(padding int) *Scaffold {
	s.statusBar.SetRightPadding(padding)
	s.notifier.Notify()
	return s
}

// AddPage registers a new tab with the given key, title, and page model.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) AddPage(key string, title string, page tea.Model) *Scaffold {
	for _, t := range s.tabBar.tabs {
		if t.key == key {
			return s
		}
	}
	s.tabBar.addTab(key, title)
	s.pages = append(s.pages, page)
	return s
}

// AddStatusItem adds a status bar item with the given key and display value.
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) AddStatusItem(key string, value string) *Scaffold {
	s.statusBar.addItem(key, value, false)
	s.notifier.Notify()
	return s
}

// AddActionableStatusItem adds a status bar item that can be drilled down (has a modal/picker).
// Setup-only: not safe to call from goroutines after Run().
func (s *Scaffold) AddActionableStatusItem(key string, value string) *Scaffold {
	s.statusBar.addItem(key, value, true)
	s.notifier.Notify()
	return s
}

// UpdateStatusItemValue updates an existing status item's displayed value,
// or adds a new one if the key doesn't exist.
// Setup-only: not safe to call from goroutines after Run().
// For runtime updates, send a StatusItemUpdateMsg via Notifier.Send().
func (s *Scaffold) UpdateStatusItemValue(key string, value string) *Scaffold {
	for _, item := range s.statusBar.items {
		if item.Key == key {
			item.Value = value
			s.statusBar.recalc()
			s.notifier.Notify()
			return s
		}
	}
	return s.AddStatusItem(key, value)
}

// --- Terminal dimensions ---

// GetTerminalWidth returns the current terminal width.
func (s *Scaffold) GetTerminalWidth() int {
	return s.width
}

// GetTerminalHeight returns the current terminal height.
func (s *Scaffold) GetTerminalHeight() int {
	return s.height
}

// GetCurrentPageKey returns the key of the currently active page.
func (s *Scaffold) GetCurrentPageKey() string {
	if s.currentTab >= 0 && s.currentTab < len(s.tabBar.tabs) {
		return s.tabBar.tabs[s.currentTab].key
	}
	return ""
}

// --- Bubble Tea interface ---

// Init satisfies tea.Model. Panics if no pages have been added.
func (s *Scaffold) Init() tea.Cmd {
	if len(s.pages) == 0 {
		panic("scaffold: no pages added, please add at least one page")
	}
	return s.notifier.Listen()
}

// Update satisfies tea.Model.
func (s *Scaffold) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !s.termReady && msg.Width > 0 && msg.Height > 0 {
			s.termReady = true
		}
		s.width = msg.Width
		s.height = msg.Height
		cmds := s.updateChildren(msg)

		return s, tea.Batch(cmds...)

	case tea.KeyMsg:
		var cmds []tea.Cmd

		// Priority 1: Modal keys (if modal visible)
		if s.pricingModal.IsVisible() {
			switch {
			case key.Matches(msg, s.KeyMap.Quit):
				return s, tea.Quit
			case msg.String() == "esc" || msg.String() == "enter":
				s.pricingModal.Hide()
				return s, nil
			default:
				// Modal consumes all other keys (focus trap)
				return s, nil
			}
		}

		// Priority 2: Status bar navigation (if in focus mode)
		if s.statusBarFocusMode {
			switch {
			case key.Matches(msg, s.KeyMap.SwitchTabRight):
				s.statusBar.SelectNext()
				return s, nil
			case key.Matches(msg, s.KeyMap.SwitchTabLeft):
				// If at first actionable item, go back to last tab
				if s.statusBar.IsAtFirstActionable() {
					s.statusBarFocusMode = false
					s.statusBar.SetFocus(false)
					s.currentTab = len(s.pages) - 1
					s.tabBar.currentTab = s.currentTab
				} else {
					s.statusBar.SelectPrev()
				}
				return s, nil
			case msg.String() == "enter":
				selectedItem := s.statusBar.GetSelectedItem()
				if selectedItem != nil && selectedItem.Key == "price" {
					s.pricingModal.Show()
				}
				return s, nil
			case msg.String() == "esc":
				s.statusBarFocusMode = false
				s.statusBar.SetFocus(false)
				return s, nil
			}
		}

		// Priority 3: Tab switching and status bar entry
		switch {
		case key.Matches(msg, s.KeyMap.Quit):
			return s, tea.Quit
		case key.Matches(msg, s.KeyMap.SwitchTabLeft):
			if s.currentTab > 0 {
				s.currentTab--
				s.tabBar.currentTab = s.currentTab
			}
		case key.Matches(msg, s.KeyMap.SwitchTabRight):
			// If at last tab and there are actionable items, enter status bar focus mode
			if s.currentTab == len(s.pages)-1 && s.statusBar.HasActionableItems() {
				s.statusBarFocusMode = true
				s.statusBar.SetFocus(true)
			} else if s.currentTab < len(s.pages)-1 {
				s.currentTab++
				s.tabBar.currentTab = s.currentTab
			}
		}
		cmds = append(cmds, s.updateChildren(msg)...)
		return s, tea.Batch(cmds...)

	case UpdateMsg:
		cmds := s.updateChildren(msg)
		cmds = append(cmds, s.notifier.Listen())
		return s, tea.Batch(cmds...)

	case TabBarSizeMsg:
		s.termSizeNotEnoughToHandleTabs = msg.NotEnoughToHandleTabs
		return s, nil

	case StatusBarSizeMsg:
		s.termSizeNotEnoughToHandleStatusBar = msg.NotEnoughToHandleStatusBar
		return s, nil

	case StatusItemUpdateMsg:
		for _, item := range s.statusBar.items {
			if item.Key == msg.Key {
				item.Value = msg.Value
				cmd := s.statusBar.recalc()
				return s, tea.Batch(cmd, s.notifier.Listen())
			}
		}
		// Key not found — add a new item (non-actionable by default).
		s.statusBar.addItem(msg.Key, msg.Value, false)
		cmd := s.statusBar.recalc()
		return s, tea.Batch(cmd, s.notifier.Listen())

	default:
		cmds := s.updateChildren(msg)
		cmds = append(cmds, s.notifier.Listen())
		return s, tea.Batch(cmds...)
	}
}

func (s *Scaffold) updateChildren(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd

	// Always update modal
	if cmd := s.pricingModal.Update(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	if cmd := s.tabBar.Update(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := s.statusBar.Update(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Broadcast non-key messages to ALL pages so that inactive pages receive
	// tool events, spinner ticks, WindowSizeMsg, etc. Key events only go to
	// the active page (and are blocked entirely when modal is visible).
	_, isKeyMsg := msg.(tea.KeyMsg)
	for i := range s.pages {
		if isKeyMsg {
			// Key events: skip inactive pages; skip all pages if modal visible
			if i != s.currentTab || s.pricingModal.IsVisible() {
				continue
			}
		}
		var pageCmd tea.Cmd
		s.pages[i], pageCmd = s.pages[i].Update(msg)
		if pageCmd != nil {
			cmds = append(cmds, pageCmd)
		}
	}

	return cmds
}

// View satisfies tea.Model.
func (s *Scaffold) View() string {
	if !s.termReady {
		return "setting up terminal..."
	}
	if s.termSizeNotEnoughToHandleTabs {
		return "terminal size is not enough to show tabs"
	}
	if s.termSizeNotEnoughToHandleStatusBar {
		return "terminal size is not enough to show status bar"
	}

	tabSection, tabLen := s.tabBar.renderTabs()
	statusSection, statusLen := s.statusBar.renderItems()
	remaining := s.width - (tabLen + statusLen + 4)
	if remaining < 0 {
		return "terminal size is not enough to show tabs and status bar"
	}

	footerBorder := lipgloss.NewStyle().Foreground(lipgloss.Color(s.borderColor))
	footView := footerBorder.Render("──") + tabSection + footerBorder.Render(strings.Repeat("─", remaining)) + statusSection + footerBorder.Render("──")

	bodyHeight := s.height - mergedHeaderHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	padTop := 0
	padBottom := 1
	if bodyHeight <= 2 {
		padTop = 0
		padBottom = 0
	}

	base := lipgloss.NewStyle().
		BorderForeground(lipgloss.Color(s.borderColor)).
		Align(s.pagePosition).
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		Width(s.width).
		PaddingTop(padTop).PaddingBottom(padBottom).
		MaxHeight(bodyHeight)

	body := s.pages[s.currentTab].View()
	if visibleBodyHeight := bodyHeight - padTop - padBottom; visibleBodyHeight > 0 && lipgloss.Height(body) < visibleBodyHeight {
		body += strings.Repeat("\n", visibleBodyHeight-lipgloss.Height(body))
	}

	baseView := lipgloss.JoinVertical(lipgloss.Top, base.Render(body), footView)

	// If modal is visible, overlay it centered on top
	if s.pricingModal.IsVisible() {
		return s.pricingModal.View()
	}

	return baseView
}
