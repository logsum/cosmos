package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ANSI color codes for terminal output (matching lipgloss colors in View)
const (
	ansiPurple = "\033[38;5;93m"  // User messages
	ansiOrange = "\033[38;5;208m" // Assistant messages
	ansiRed    = "\033[38;5;196m" // Error messages
	ansiGreen  = "\033[38;5;46m"  // Success
	ansiYellow = "\033[38;5;226m" // Running/spinner
	ansiDim    = "\033[38;5;245m" // Dim/gray text
	ansiReset  = "\033[0m"        // Reset color
	ansiBold   = "\033[1m"        // Bold text
)

func getGlamourRenderer(width int) (*glamour.TermRenderer, error) {
	// Create a new renderer with the specified width
	// This ensures correct wrapping even after terminal resize
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating glamour renderer: %w", err)
	}
	return renderer, nil
}

type toolState int

const (
	toolRunning toolState = iota
	toolSuccess
	toolFailed
)

type toolInfo struct {
	state  toolState
	callID string
	name   string
	params []string // formatted key: value lines, max 3
}

type chatMessage struct {
	text      string
	isUser    bool
	isError   bool
	isTool    bool
	isWarning bool // for system warnings (context, etc.)
	tool      *toolInfo

	// Markdown rendering cache
	renderedLines []string // Cached ANSI-rendered output (nil until finalized)
	renderError   error    // non-nil if glamour rendering failed
}

type ChatModel struct {
	session SessionSubmitter // Session for submitting messages

	messages               []chatMessage
	accumulatedText        string // Buffer for current assistant message
	assistantHeaderPrinted bool   // Whether we've printed the bar for assistant
	width                  int
	height                 int
	splashPrinted          bool
	flushedLineCount       int // Number of rendered lines flushed to stdout

	spinner        spinner.Model
	spinnerRunning bool // Whether we have an active spinner tick cmd
}

func NewChatModel(session SessionSubmitter) *ChatModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	return &ChatModel{
		session: session,
		spinner: sp,
	}
}

// renderMessageMarkdown renders markdown text to ANSI-formatted lines.
func (m *ChatModel) renderMessageMarkdown(text string) ([]string, error) {
	availableWidth := m.width - 2 // Account for "▌ " bar prefix
	if availableWidth < 20 {
		availableWidth = 20
	}

	renderer, err := getGlamourRenderer(availableWidth)
	if err != nil {
		return nil, fmt.Errorf("initializing glamour: %w", err)
	}

	rendered, err := renderer.Render(text)
	if err != nil {
		return nil, fmt.Errorf("rendering markdown: %w", err)
	}

	// Split into lines (one per terminal line)
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")

	// Remove empty leading/trailing lines
	lines = trimEmptyLines(lines)

	return lines, nil
}

// finalizeAccumulatedText renders the current accumulatedText as a markdown
// assistant message, appends it to m.messages, and resets the accumulator.
// This is a no-op if accumulatedText is empty.
func (m *ChatModel) finalizeAccumulatedText() {
	if m.accumulatedText == "" {
		return
	}
	msg := chatMessage{
		text:   m.accumulatedText,
		isUser: false,
	}
	renderedLines, err := m.renderMessageMarkdown(m.accumulatedText)
	if err == nil {
		msg.renderedLines = renderedLines
	} else {
		msg.renderError = err
	}
	m.messages = append(m.messages, msg)
	m.accumulatedText = ""
	m.assistantHeaderPrinted = false
}

func trimEmptyLines(lines []string) []string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}

	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}

	if start >= end {
		return []string{""}
	}
	return lines[start:end]
}

func (m *ChatModel) Init() tea.Cmd {
	return nil
}

// hasRunningTools returns true if any tool message is still in running state.
func (m *ChatModel) hasRunningTools() bool {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].tool != nil && m.messages[i].tool.state == toolRunning {
			return true
		}
	}
	return false
}

func (m *ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ============================================================================
	// STDOUT OUTPUT STRATEGY: LINE-LEVEL FLUSHING
	// ============================================================================
	//
	// Messages are kept in memory (m.messages) and displayed in View() which shows
	// the last N lines that fit on screen. As new content arrives and lines scroll
	// OFF the visible window, we flush ONLY those specific lines to stdout (not
	// entire messages).
	//
	// Key behaviors:
	//  - Long message partially visible? Only scrolled-off lines are flushed
	//  - Terminal scrollback contains exactly what scrolled out, line by line
	//  - Formatting (colored bars, wrapping, newlines) preserved in stdout
	//  - No duplication: each line flushed exactly once
	//
	// How it works:
	//  1. ChatTokenMsg: Accumulate in m.accumulatedText (NO stdout yet)
	//  2. View(): Show completed messages + streaming message
	//  3. flushOldMessages(): Calculate scrolled-off lines, flush to stdout
	//  4. ChatCompletionMsg: Finalize message, trigger flush
	//
	// CRITICAL: buildAllRenderedLines() must include streaming message to match
	// View() exactly, or line counts break. See warnings in those functions.
	//
	// See CLAUDE.md "Terminal Scrollback Strategy" for full documentation.
	// ============================================================================

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		// Only advance spinner if we have running tools
		if !m.hasRunningTools() {
			m.spinnerRunning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Do NOT call flushOldMessages on spinner ticks — they only re-render View()
		return m, cmd

	case PromptSubmitMsg:
		// Append user message to display immediately
		firstPrompt := len(m.messages) == 0
		userMsg := chatMessage{
			text:   msg.Value,
			isUser: true,
		}
		m.messages = append(m.messages, userMsg)

		var cmds []tea.Cmd
		if firstPrompt && !m.splashPrinted {
			cmds = append(cmds, tea.Printf("%s", NewSplash().View()), tea.Printf(""))
			m.splashPrinted = true
		}

		// Submit to core session (async)
		if m.session != nil {
			m.session.SubmitMessage(msg.Value)
		}

		// Start accumulating assistant response
		m.accumulatedText = ""
		m.assistantHeaderPrinted = false

		// Check if old messages need to be flushed to stdout (scrolled off screen)
		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			cmds = append(cmds, flushCmd)
		}

		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Sequence(cmds...)

	case ChatTokenMsg:
		// Accumulate tokens from streaming - they will be displayed in View()
		m.accumulatedText += msg.Text
		// The streaming effect happens in View() by showing accumulatedText
		// No stdout printing here - that only happens when messages scroll off
		return m, nil

	case ChatCompletionMsg:
		// Finalize the assistant message — but only if there's text to finalize.
		// After a tool-use turn, accumulatedText is already "" (reset by
		// ChatToolResultMsg), so finalizeAccumulatedText is a no-op.
		m.finalizeAccumulatedText()

		// Check if this new message pushed older messages off-screen that need flushing
		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}

		return m, nil

	case ChatToolUseMsg:
		// Finalize any in-progress assistant text before showing tool use
		m.finalizeAccumulatedText()

		params := formatToolParams(msg.Input, 3)
		toolMsg := chatMessage{
			isTool: true,
			tool: &toolInfo{
				state:  toolRunning,
				callID: msg.ToolCallID,
				name:   msg.ToolName,
				params: params,
			},
		}
		m.messages = append(m.messages, toolMsg)

		var cmds []tea.Cmd

		// Start spinner if not already running
		if !m.spinnerRunning {
			m.spinnerRunning = true
			cmds = append(cmds, m.spinner.Tick)
		}

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			cmds = append(cmds, flushCmd)
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case ChatToolResultMsg:
		// Find matching tool message by callID and update its state
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].tool != nil && m.messages[i].tool.callID == msg.ToolCallID {
				if msg.IsError {
					m.messages[i].tool.state = toolFailed
				} else {
					m.messages[i].tool.state = toolSuccess
				}
				// Collapse params on completion
				m.messages[i].tool.params = nil
				break
			}
		}

		// Reset accumulator for the next assistant turn
		m.accumulatedText = ""
		m.assistantHeaderPrinted = false

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}
		return m, nil

	case ChatErrorMsg:
		// Display error as a system message
		errorText := "Error: " + msg.Error
		errorMsg := chatMessage{
			text:    errorText,
			isUser:  false,
			isError: true,
		}
		m.messages = append(m.messages, errorMsg)
		m.accumulatedText = ""
		m.assistantHeaderPrinted = false

		// Check if older messages need to be flushed to stdout
		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}

		return m, nil

	case ChatContextWarningMsg:
		m.finalizeAccumulatedText()

		// Add warning message
		warning := fmt.Sprintf(
			"⚠ Context usage at %.0f%%. Consider running /compact to reduce token usage.",
			msg.Percentage,
		)
		m.messages = append(m.messages, chatMessage{
			text:      warning,
			isWarning: true,
		})

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}
		return m, nil

	case ChatContextAutoCompactMsg:
		m.finalizeAccumulatedText()

		// Add auto-compact notification
		notice := fmt.Sprintf(
			"🔧 Context usage reached %.0f%%. Automatically compacting conversation...",
			msg.Percentage,
		)
		m.messages = append(m.messages, chatMessage{
			text:      notice,
			isWarning: true,
		})

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}
		return m, nil

	case ChatCompactionStartMsg:
		m.finalizeAccumulatedText()

		// Show compaction start message
		mode := "Compacting"
		if msg.Mode == "automatic" {
			mode = "Auto-compacting"
		}
		m.messages = append(m.messages, chatMessage{
			text:      fmt.Sprintf("⏳ %s conversation...", mode),
			isWarning: true,
		})

		// Start spinner
		var cmds []tea.Cmd
		if !m.spinnerRunning {
			m.spinnerRunning = true
			cmds = append(cmds, m.spinner.Tick)
		}
		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			cmds = append(cmds, flushCmd)
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case ChatCompactionProgressMsg:
		// Update the last warning message with progress stage
		stageText := msg.Stage
		switch msg.Stage {
		case "generating_summary":
			stageText = "Generating summary..."
		case "estimating_tokens":
			stageText = "Estimating token savings..."
		}
		// Replace the last warning message (compaction start) with progress
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].isWarning {
				m.messages[i].text = "⏳ " + stageText
				break
			}
		}
		return m, nil

	case ChatCompactionCompleteMsg:
		m.finalizeAccumulatedText()

		// Calculate reduction percentage
		reduction := 100.0 * float64(msg.OldTokens-msg.NewTokens) / float64(msg.OldTokens)
		success := fmt.Sprintf(
			"✓ Compacted from %s to %s tokens (%.0f%% reduction)",
			formatCount(msg.OldTokens),
			formatCount(msg.NewTokens),
			reduction,
		)
		m.messages = append(m.messages, chatMessage{
			text:      success,
			isWarning: true,
		})

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}
		return m, nil

	case ChatCompactionFailedMsg:
		m.finalizeAccumulatedText()

		// Show error message
		errorMsg := fmt.Sprintf("✗ Compaction failed: %s. Conversation preserved.", msg.Error)
		m.messages = append(m.messages, chatMessage{
			text:      errorMsg,
			isWarning: true,
		})

		flushCmd := m.flushOldMessages()
		if flushCmd != nil {
			return m, flushCmd
		}
		return m, nil
	}
	return m, nil
}

func (m *ChatModel) View() string {
	// ============================================================================
	// CRITICAL: Terminal Scrollback Strategy - DO NOT MODIFY WITHOUT READING
	// ============================================================================
	//
	// This View() does NOT implement scrolling. We rely on the terminal's native
	// scrollback (iTerm, etc.) for scrolling. See main.go for why we don't use
	// tea.WithAltScreen(). DO NOT add scroll offset logic here.
	//
	// CRITICAL INVARIANT: This View() MUST produce the EXACT same visual output
	// as buildAllRenderedLines() (but View uses lipgloss, buildAllRenderedLines
	// uses ANSI codes). If they diverge, line-level flushing breaks.
	//
	// Specifically:
	//  - MUST show completed messages (m.messages)
	//  - MUST show streaming message (m.accumulatedText) if non-empty
	//  - MUST use same wrapText() and same availableWidth
	//  - MUST add blank lines only after assistant/error messages
	//
	// See CLAUDE.md "Terminal Scrollback Strategy" for full details.
	// ============================================================================

	if len(m.messages) == 0 && m.accumulatedText == "" {
		return NewSplash().View()
	}

	userBar := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("93")).Render("▌")
	replyBar := lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("▌")
	errorBar := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("▌")

	successIcon := lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Render("✓")
	failIcon := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✗")
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// Account for bar ("▌ ") prefix when wrapping
	prefixWidth := 2
	availableWidth := m.width - prefixWidth
	if availableWidth < 20 {
		availableWidth = 20 // minimum width
	}

	var content strings.Builder

	// Render completed messages
	for _, msg := range m.messages {
		if msg.isWarning {
			// Warning messages use yellow bar (226), no blank line after
			warningBar := lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Render("▌")
			wrappedLines := wrapText(msg.text, availableWidth)
			for i, line := range wrappedLines {
				if i == 0 {
					content.WriteString(warningBar + " " + line + "\n")
				} else {
					content.WriteString("  " + line + "\n")
				}
			}
			content.WriteString("\n") // blank separator
			continue
		}

		if msg.isTool && msg.tool != nil {
			// Tool-specific rendering: no bar, compact display
			switch msg.tool.state {
			case toolRunning:
				// Spinner + name
				content.WriteString(m.spinner.View() + " " + msg.tool.name + "\n")
				// Indented params (max 3 lines)
				for _, p := range msg.tool.params {
					content.WriteString("    " + dimStyle.Render(p) + "\n")
				}
			case toolSuccess:
				content.WriteString(successIcon + " " + msg.tool.name + "\n")
			case toolFailed:
				content.WriteString(failIcon + " " + msg.tool.name + "\n")
			}
			content.WriteString("\n") // blank separator
			continue
		}

		var wrappedLines []string
		if len(msg.renderedLines) > 0 {
			// Use cached glamour rendering
			wrappedLines = msg.renderedLines
		} else {
			// Fallback to plain text wrapping
			wrappedLines = wrapText(msg.text, availableWidth)
		}

		if msg.isUser {
			for i, line := range wrappedLines {
				if i == 0 {
					content.WriteString(userBar + " " + line + "\n")
				} else {
					content.WriteString("  " + line + "\n")
				}
			}
		} else {
			// Choose bar based on whether it's an error
			bar := replyBar
			if msg.isError {
				bar = errorBar
			}
			for i, line := range wrappedLines {
				if i == 0 {
					content.WriteString(bar + " " + line + "\n")
				} else {
					content.WriteString("  " + line + "\n")
				}
			}
			content.WriteString("\n")
		}
	}

	// Render streaming message (if any)
	if m.accumulatedText != "" {
		wrappedLines := wrapText(m.accumulatedText, availableWidth)
		for i, line := range wrappedLines {
			if i == 0 {
				content.WriteString(replyBar + " " + line + "\n")
			} else {
				content.WriteString("  " + line + "\n")
			}
		}
		// Don't add blank line after streaming message (it's incomplete)
	}

	visibleLines := m.visibleBodyLines()
	if visibleLines <= 0 {
		return content.String()
	}

	lines := strings.Split(strings.TrimRight(content.String(), "\n"), "\n")
	if len(lines) > visibleLines {
		lines = lines[len(lines)-visibleLines:]
	}
	return strings.Join(lines, "\n")
}

// wrapText wraps text to fit within the specified width, preserving newlines.
// Each line in the input is wrapped independently, and empty lines are preserved.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	// Split by newlines first to preserve paragraph structure
	inputLines := strings.Split(text, "\n")
	var result []string

	for _, line := range inputLines {
		// Empty lines are preserved as-is
		if strings.TrimSpace(line) == "" {
			result = append(result, "")
			continue
		}

		// Wrap this line
		wrapped := wrapLine(line, width)
		result = append(result, wrapped...)
	}

	if len(result) == 0 {
		return []string{""}
	}

	return result
}

// wrapLine wraps a single line (no newlines) to fit within the specified width
func wrapLine(line string, width int) []string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		// If word itself is longer than width, we need to break it
		if len([]rune(word)) > width {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
			// Break long word into chunks
			runes := []rune(word)
			for len(runes) > width {
				lines = append(lines, string(runes[:width]))
				runes = runes[width:]
			}
			if len(runes) > 0 {
				currentLine.WriteString(string(runes))
			}
			continue
		}

		testLine := word
		if currentLine.Len() > 0 {
			testLine = currentLine.String() + " " + word
		}

		if len([]rune(testLine)) <= width {
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
			}
			currentLine.WriteString(word)
		} else {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
			currentLine.WriteString(word)
		}
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	if len(lines) == 0 {
		return []string{""}
	}

	return lines
}

func (m *ChatModel) visibleBodyLines() int {
	if m.height <= 0 {
		return 0
	}

	bodyHeight := m.height - mergedHeaderHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	padTop := 0
	padBottom := 1
	if bodyHeight <= 2 {
		padTop = 0
		padBottom = 0
	}

	visible := bodyHeight - padTop - padBottom
	if visible < 1 {
		return 1
	}
	return visible
}

// flushOldMessages writes lines that have scrolled off the visible View() to stdout.
//
// ============================================================================
// LINE-LEVEL FLUSHING: Not message-level!
// ============================================================================
//
// This function flushes at the LINE level, not the message level. If a long message
// is half-visible (top half scrolled off, bottom half still visible), ONLY the
// scrolled-off lines are written to stdout. This prevents duplication.
//
// Algorithm:
//  1. buildAllRenderedLines() -> get ALL lines (including streaming message!)
//  2. totalLines - visibleLines -> calculate firstVisibleLine
//  3. Flush lines[flushedLineCount:firstVisibleLine] to stdout
//  4. Update m.flushedLineCount = firstVisibleLine
//
// CRITICAL: buildAllRenderedLines() must match View() exactly, including the
// streaming message (m.accumulatedText), or line counts will be wrong.
//
// See CLAUDE.md "Terminal Scrollback Strategy" for full architecture.
// ============================================================================
func (m *ChatModel) flushOldMessages() tea.Cmd {
	if len(m.messages) == 0 {
		return nil // Nothing to flush
	}

	// Calculate how many lines the current View() can display
	visibleLines := m.visibleBodyLines()
	if visibleLines <= 0 {
		return nil
	}

	availableWidth := m.width - 2 // Account for "▌ " prefix
	if availableWidth < 20 {
		availableWidth = 20
	}

	// Build ALL rendered lines from all messages
	allLines := m.buildAllRenderedLines(availableWidth)
	totalLines := len(allLines)

	// Calculate which lines are off-screen
	firstVisibleLine := 0
	if totalLines > visibleLines {
		firstVisibleLine = totalLines - visibleLines
	}

	// Flush lines that are off-screen but not yet flushed
	if firstVisibleLine <= m.flushedLineCount {
		return nil // Nothing new to flush
	}

	var toFlush strings.Builder
	for i := m.flushedLineCount; i < firstVisibleLine; i++ {
		toFlush.WriteString(allLines[i])
		toFlush.WriteString("\n")
	}

	m.flushedLineCount = firstVisibleLine

	if toFlush.Len() == 0 {
		return nil
	}

	return tea.Printf("%s", toFlush.String())
}

// buildAllRenderedLines builds the complete rendered output (with ANSI colors and bars)
// for all messages as individual lines, ready for stdout flushing.
//
// ============================================================================
// CRITICAL INVARIANT: MUST match View() output exactly!
// ============================================================================
//
// This function produces the EXACT same visual output as View() but using ANSI codes
// instead of lipgloss, and returns it as a slice where each element is one terminal line.
//
// IF THIS DIVERGES FROM View(), line counting breaks and scrollback flushing duplicates
// or misses lines. When modifying View(), update this function identically.
//
// Requirements:
//  1. MUST include completed messages (m.messages)
//  2. MUST include streaming message (m.accumulatedText) if non-empty <- CRITICAL!
//  3. MUST use same wrapText() with same availableWidth
//  4. MUST add blank lines only after assistant/error messages (not user messages)
//  5. MUST use ANSI color codes (not lipgloss) since output goes to stdout
//
// See CLAUDE.md "Terminal Scrollback Strategy" for architecture.
// ============================================================================
func (m *ChatModel) buildAllRenderedLines(availableWidth int) []string {
	var lines []string

	// Render completed messages
	for _, msg := range m.messages {
		if msg.isWarning {
			// Warning bar with ANSI yellow (226)
			bar := ansiYellow + "▌" + ansiReset
			wrappedLines := wrapText(msg.text, availableWidth)
			for i, line := range wrappedLines {
				if i == 0 {
					lines = append(lines, bar+" "+line)
				} else {
					lines = append(lines, "  "+line)
				}
			}
			lines = append(lines, "") // blank separator
			continue
		}

		if msg.isTool && msg.tool != nil {
			// Tool-specific rendering: no bar, compact display
			switch msg.tool.state {
			case toolRunning:
				// Use a static spinner frame for stdout (yellow braille dot)
				lines = append(lines, ansiYellow+"⠋"+ansiReset+" "+msg.tool.name)
				for _, p := range msg.tool.params {
					lines = append(lines, "    "+ansiDim+p+ansiReset)
				}
			case toolSuccess:
				lines = append(lines, ansiGreen+"✓"+ansiReset+" "+msg.tool.name)
			case toolFailed:
				lines = append(lines, ansiRed+"✗"+ansiReset+" "+msg.tool.name)
			}
			lines = append(lines, "") // blank separator
			continue
		}

		var bar string
		if msg.isUser {
			bar = ansiBold + ansiPurple + "▌" + ansiReset
		} else if msg.isError {
			bar = ansiRed + "▌" + ansiReset
		} else {
			bar = ansiOrange + "▌" + ansiReset
		}

		var wrappedLines []string
		if len(msg.renderedLines) > 0 {
			// Use cached glamour rendering
			wrappedLines = msg.renderedLines
		} else {
			// Fallback to plain text wrapping
			wrappedLines = wrapText(msg.text, availableWidth)
		}

		for i, line := range wrappedLines {
			if i == 0 {
				lines = append(lines, bar+" "+line)
			} else {
				lines = append(lines, "  "+line)
			}
		}

		// Only add blank line after assistant/error messages
		if !msg.isUser {
			lines = append(lines, "")
		}
	}

	// Render streaming message (if any) - MUST match View() behavior!
	if m.accumulatedText != "" {
		bar := ansiOrange + "▌" + ansiReset
		wrappedLines := wrapText(m.accumulatedText, availableWidth)
		for i, line := range wrappedLines {
			if i == 0 {
				lines = append(lines, bar+" "+line)
			} else {
				lines = append(lines, "  "+line)
			}
		}
		// No blank line after streaming message (it's incomplete)
	}

	return lines
}

// formatToolParams parses JSON input and formats key-value pairs for display.
// Returns at most maxLines formatted strings. If there are more entries than
// maxLines, the last line indicates how many were omitted.
func formatToolParams(jsonInput string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(jsonInput), &data); err != nil {
		// JSON parse failed — return truncated raw input
		truncated := jsonInput
		if len(truncated) > 60 {
			truncated = truncated[:57] + "..."
		}
		return []string{truncated}
	}

	if len(data) == 0 {
		return nil
	}

	// Sort keys for stable output
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, k := range keys {
		v := formatParamValue(data[k])
		lines = append(lines, k+": "+v)
	}

	if len(lines) <= maxLines {
		return lines
	}

	// Truncate: show maxLines-1 entries + "... and N more"
	result := lines[:maxLines-1]
	remaining := len(lines) - (maxLines - 1)
	result = append(result, fmt.Sprintf("... and %d more", remaining))
	return result
}

// formatParamValue converts a value to a display string, truncating long values.
func formatParamValue(v any) string {
	var s string
	switch val := v.(type) {
	case string:
		s = `"` + val + `"`
	case nil:
		s = "null"
	default:
		b, err := json.Marshal(val)
		if err != nil {
			s = fmt.Sprintf("%v", val)
		} else {
			s = string(b)
		}
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// formatCount formats a token count with K/M abbreviations.
// This is a copy of core.formatCount for UI display (core cannot be imported here
// due to dependency rules). If you modify this logic, update core/pricing.go as well.
func formatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		k := float64(n) / 1000
		if k >= 999.95 {
			return "1M"
		}
		s := fmt.Sprintf("%.1fK", k)
		return strings.Replace(s, ".0K", "K", 1)
	}
	m := float64(n) / 1_000_000
	s := fmt.Sprintf("%.1fM", m)
	return strings.Replace(s, ".0M", "M", 1)
}
