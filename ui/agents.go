package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tool represents a tool that agents can use
type Tool struct {
	Name        string
	Description string
}

// AgentExecution represents a single agent execution log entry
type AgentExecution struct {
	ToolCallID  string
	Timestamp   string
	AgentName   string
	Description string
	Tools       []string
	Status      string // "success", "running", "failed"
	Details     string
	Expanded    bool
}

type viewMode int

const (
	viewModeHistory viewMode = iota
	viewModeTools
	viewModeCreate
)

// toolActivity tracks live usage stats for a registered tool.
type toolActivity struct {
	CallCount  int
	LastStatus string // "success", "failed", "running", or ""
	LastCall   string // timestamp "15:04:05"
}

type AgentsModel struct {
	scaffold       *Scaffold
	mode           viewMode
	cursor         int
	executions     []AgentExecution
	availableTools []Tool
	toolStats      map[string]*toolActivity
	message        string
	width          int
	height         int
	scrollOffset   int
	detailsFocused bool
}

func NewAgentsModel(scaffold *Scaffold, tools []Tool) *AgentsModel {
	return &AgentsModel{
		scaffold:       scaffold,
		mode:           viewModeHistory,
		executions:     []AgentExecution{},
		availableTools: tools,
		toolStats:      make(map[string]*toolActivity),
	}
}

func (m *AgentsModel) Init() tea.Cmd {
	return nil
}

func (m *AgentsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case ChatToolUseMsg:
		// Prepend a running entry immediately
		desc := msg.ToolName
		if msg.Input != "" {
			truncInput := msg.Input
			if len(truncInput) > 60 {
				truncInput = truncInput[:57] + "..."
			}
			desc = msg.ToolName + " " + truncInput
		}
		entry := AgentExecution{
			ToolCallID:  msg.ToolCallID,
			Timestamp:   time.Now().Format("15:04:05"),
			AgentName:   msg.ToolName,
			Description: desc,
			Tools:       []string{msg.ToolName},
			Status:      "running",
			Details:     "Input:\n" + msg.Input,
		}
		m.executions = append([]AgentExecution{entry}, m.executions...)
		// Adjust cursor if needed (items shifted down)
		if m.cursor > 0 {
			m.cursor++
		}
		// Track tool activity
		stats := m.getOrCreateStats(msg.ToolName)
		stats.CallCount++
		stats.LastStatus = "running"
		stats.LastCall = time.Now().Format("15:04:05")
		return m, nil

	case ChatToolResultMsg:
		// Find matching running entry and update it
		for i := range m.executions {
			if m.executions[i].ToolCallID == msg.ToolCallID && m.executions[i].Status == "running" {
				if msg.IsError {
					m.executions[i].Status = "failed"
				} else {
					m.executions[i].Status = "success"
				}
				m.executions[i].Details += "\n\nOutput:\n" + msg.Result
				break
			}
		}
		// Track tool activity
		if stats, ok := m.toolStats[msg.ToolName]; ok {
			if msg.IsError {
				stats.LastStatus = "failed"
			} else {
				stats.LastStatus = "success"
			}
		}
		return m, nil

	case ToolExecutionMsg:
		// The ToolExecutionMsg carries complete data. If we already have a
		// matching entry (from ChatToolUseMsg + ChatToolResultMsg), update it
		// with the full details. Otherwise prepend a new completed entry.
		found := false
		for i := range m.executions {
			if m.executions[i].ToolCallID == msg.ToolCallID {
				if msg.IsError {
					m.executions[i].Status = "failed"
				} else {
					m.executions[i].Status = "success"
				}
				m.executions[i].Details = "Input:\n" + msg.Input + "\n\nOutput:\n" + msg.Output
				found = true
				break
			}
		}
		if !found {
			status := "success"
			if msg.IsError {
				status = "failed"
			}
			entry := AgentExecution{
				ToolCallID:  msg.ToolCallID,
				Timestamp:   time.Now().Format("15:04:05"),
				AgentName:   msg.ToolName,
				Description: msg.ToolName,
				Tools:       []string{msg.ToolName},
				Status:      status,
				Details:     "Input:\n" + msg.Input + "\n\nOutput:\n" + msg.Output,
			}
			m.executions = append([]AgentExecution{entry}, m.executions...)
		}
		// Track tool activity
		stats := m.getOrCreateStats(msg.ToolName)
		if msg.IsError {
			stats.LastStatus = "failed"
		} else {
			stats.LastStatus = "success"
		}
		if stats.LastCall == "" {
			stats.CallCount++
			stats.LastCall = time.Now().Format("15:04:05")
		}
		return m, nil

	case PromptSubmitMsg:
		if m.mode == viewModeCreate && msg.Value != "" {
			// Create a new agent with the provided prompt
			newExecution := AgentExecution{
				Timestamp:   time.Now().Format("2006-01-02 15:04:05"),
				AgentName:   "custom-agent",
				Description: msg.Value,
				Tools:       []string{"pending"},
				Status:      "running",
				Details:     "Agent created from prompt. Execution pending...",
			}
			m.executions = append([]AgentExecution{newExecution}, m.executions...)
			m.message = "✓ Agent created: " + msg.Value
			m.mode = viewModeHistory
			m.cursor = 0
			m.scrollOffset = 0
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "1":
			m.mode = viewModeHistory
			m.cursor = 0
			m.scrollOffset = 0
		case "2":
			m.mode = viewModeTools
			m.cursor = 0
			m.scrollOffset = 0
		case "3":
			m.mode = viewModeCreate
			m.cursor = 0
			m.scrollOffset = 0

		case "up":
			if m.mode == viewModeHistory {
				if m.detailsFocused {
					m.detailsFocused = false
				} else if m.cursor > 0 {
					m.cursor--
					m.adjustScroll()
				}
			} else if m.mode == viewModeTools {
				if m.cursor > 0 {
					m.cursor--
					m.adjustScroll()
				}
			}

		case "down":
			if m.mode == viewModeHistory {
				if len(m.executions) > 0 && m.executions[m.cursor].Expanded && !m.detailsFocused {
					m.detailsFocused = true
				} else {
					m.detailsFocused = false
					if m.cursor < len(m.executions)-1 {
						m.cursor++
						m.adjustScroll()
					}
				}
			} else if m.mode == viewModeTools {
				if m.cursor < len(m.availableTools)-1 {
					m.cursor++
					m.adjustScroll()
				}
			}

		case "enter":
			if m.mode == viewModeHistory && len(m.executions) > 0 {
				if m.detailsFocused {
					m.message = "✓ Execution details for " + m.executions[m.cursor].AgentName
					m.detailsFocused = false
				} else {
					m.executions[m.cursor].Expanded = !m.executions[m.cursor].Expanded
				}
			}
		}
	}
	return m, nil
}

func (m *AgentsModel) getOrCreateStats(toolName string) *toolActivity {
	stats, ok := m.toolStats[toolName]
	if !ok {
		stats = &toolActivity{}
		m.toolStats[toolName] = stats
	}
	return stats
}

func (m *AgentsModel) adjustScroll() {
	visibleLines := m.getVisibleLines()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	} else if m.cursor >= m.scrollOffset+visibleLines {
		m.scrollOffset = m.cursor - visibleLines + 1
	}
}

func (m *AgentsModel) getVisibleLines() int {
	if m.height <= 0 {
		return 10
	}
	bodyHeight := m.height - mergedHeaderHeight
	if bodyHeight < 5 {
		return 5
	}
	return bodyHeight - 5 // Leave room for header and footer
}

func (m *AgentsModel) View() string {
	orangeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("208"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("93"))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	failStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	runningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))

	var b strings.Builder

	// Navigation tabs
	tabs := []string{"[1] History", "[2] Tools", "[3] Create"}
	var tabsRendered []string
	for i, tab := range tabs {
		if viewMode(i) == m.mode {
			tabsRendered = append(tabsRendered, orangeStyle.Render(tab))
		} else {
			tabsRendered = append(tabsRendered, dimStyle.Render(tab))
		}
	}
	b.WriteString(strings.Join(tabsRendered, "  "))
	b.WriteString("\n\n")

	switch m.mode {
	case viewModeHistory:
		b.WriteString(headerStyle.Render("Execution History"))
		b.WriteString("\n\n")

		if len(m.executions) == 0 {
			b.WriteString(dimStyle.Render("  No executions yet."))
		} else {
			pipeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
			pipe := pipeStyle.Render("│")

			for i, exec := range m.executions {
				isCursor := i == m.cursor
				onHeader := isCursor && !m.detailsFocused

				arrow := "▸"
				if exec.Expanded {
					arrow = "▾"
				}

				prefix := "  "
				if onHeader {
					prefix = "> "
				}

				statusIcon := ""
				statusStyle := dimStyle
				switch exec.Status {
				case "success":
					statusIcon = "✓"
					statusStyle = successStyle
				case "failed":
					statusIcon = "✗"
					statusStyle = failStyle
				case "running":
					statusIcon = "◌"
					statusStyle = runningStyle
				}

				line := fmt.Sprintf("%s %s %s  %s", arrow, statusStyle.Render(statusIcon), exec.Timestamp, exec.Description)
				if onHeader {
					b.WriteString(orangeStyle.Render(prefix + line))
				} else {
					b.WriteString(dimStyle.Render(prefix + line))
				}
				b.WriteString("\n")

				if exec.Expanded {
					b.WriteString("  " + pipe + "  " + dimStyle.Render("Agent: "+exec.AgentName) + "\n")
					b.WriteString("  " + pipe + "  " + dimStyle.Render("Tools: "+strings.Join(exec.Tools, ", ")) + "\n")
					b.WriteString("  " + pipe + "\n")
					for _, dl := range strings.Split(exec.Details, "\n") {
						b.WriteString("  " + pipe + "  " + dimStyle.Render(dl) + "\n")
					}
					b.WriteString("  " + pipe + "\n")
				}
			}

			b.WriteString("\n")
			b.WriteString(dimStyle.Render("  ↑↓ navigate   Enter expand/collapse"))
		}

	case viewModeTools:
		b.WriteString(headerStyle.Render("Available Tools"))
		b.WriteString("\n\n")

		for i, tool := range m.availableTools {
			isCursor := i == m.cursor
			prefix := "  "
			if isCursor {
				prefix = "> "
			}

			stats := m.toolStats[tool.Name]
			bullet := dimStyle.Render("○")
			if stats != nil && stats.CallCount > 0 {
				bullet = orangeStyle.Render("●")
			}

			nameDesc := fmt.Sprintf("%-20s %s", tool.Name, tool.Description)
			if isCursor {
				b.WriteString(orangeStyle.Render(prefix) + bullet + " " + orangeStyle.Render(nameDesc))
			} else {
				b.WriteString(dimStyle.Render(prefix) + bullet + " " + dimStyle.Render(nameDesc))
			}
			b.WriteString("\n")

			// Activity line
			if stats != nil && stats.CallCount > 0 {
				callWord := "calls"
				if stats.CallCount == 1 {
					callWord = "call"
				}
				statusIcon := "✓"
				statusRender := successStyle.Render(statusIcon)
				switch stats.LastStatus {
				case "failed":
					statusRender = failStyle.Render("✗")
				case "running":
					statusRender = runningStyle.Render("◌")
				}
				activity := fmt.Sprintf("    %d %s · last: ", stats.CallCount, callWord)
				b.WriteString(dimStyle.Render(activity) + statusRender + " " + dimStyle.Render(stats.LastCall))
			} else {
				b.WriteString(dimStyle.Render("    No calls yet"))
			}
			b.WriteString("\n")
		}

		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  ↑↓ navigate"))

	case viewModeCreate:
		b.WriteString(headerStyle.Render("Create New Agent"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Enter a prompt to create a custom agent that will execute"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("the requested task using available tools."))
		b.WriteString("\n\n")
		b.WriteString(orangeStyle.Render("Examples:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • \"Analyze all Go files and report code quality metrics\""))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • \"Find all TODO comments and create a task list\""))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  • \"Run tests and generate coverage report\""))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Type your prompt below and press Enter to create the agent."))
	}

	if m.message != "" {
		b.WriteString("\n\n")
		b.WriteString(orangeStyle.Render("  " + m.message))
	}

	return b.String()
}
