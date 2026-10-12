package tui

import (
	"fmt"
	"os"
	"remoteops/internal/i18n"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
)

func consentLines(s client.Snapshot, now time.Time) []string {
	lines := append(messages(s), []string{i18n.Text(s.Language, "worker.ui.review"), i18n.Text(s.Language, "worker.ui.controller") + client.SafeText(s.Controller), i18n.Text(s.Language, "worker.ui.address") + client.SafeText(s.Address), i18n.Text(s.Language, "worker.ui.duration") + s.Authorization.Round(time.Second).String(), i18n.Text(s.Language, "worker.ui.pairing_remaining") + remaining(s.PairingExpiresAt, now, s.Language), i18n.Text(s.Language, "worker.ui.scope")}...)
	for _, item := range s.Scope {
		lines = append(lines, "• "+client.SafeText(item))
	}
	return append(lines, i18n.Text(s.Language, "worker.ui.trust"), i18n.Text(s.Language, "worker.ui.explicit"))
}
func remaining(until, now time.Time, language ...i18n.Language) string {
	if until.IsZero() {
		return i18n.Text(displayLanguage(language), "worker.ui.waiting")
	}
	return max(until.Sub(now), 0).Round(time.Second).String()
}

func messages(s client.Snapshot) []string {
	var lines []string
	for _, text := range []string{s.Warning, s.Error} {
		if text != "" {
			lines = append(lines, client.SafeText(text))
		}
	}
	return lines
}
func displayLanguage(language []i18n.Language) i18n.Language {
	if len(language) > 0 {
		return language[0]
	}
	return i18n.Chinese
}
func (m *model) body() []string {
	if m.languageMenu {
		return append(messages(m.snapshot), i18n.Text(m.snapshot.Language, "worker.tui.language_menu"))
	}
	if m.screen == consentScreen {
		return m.contentBody()
	}
	return append(messages(m.snapshot), m.contentBody()...)
}
func (m *model) contentBody() []string {
	switch m.screen {
	case pathScreen:
		// Only show a tail of long paths so the cursor remains reachable at 60 cells.
		path := client.SafeText(string(m.input))
		budget := max(m.contentWidth()-2, 1)
		if width := lipgloss.Width(path); width > budget {
			path = ansi.TruncateLeft(path, width-budget+1, "…")
		}
		lines := []string{i18n.Text(m.snapshot.Language, "worker.tui.path_step"), i18n.Text(m.snapshot.Language, "worker.tui.path_hint"), "> " + path}
		return lines
	case loadingScreen:
		return []string{i18n.Text(m.snapshot.Language, "worker.tui.loading"), i18n.Text(m.snapshot.Language, "worker.tui.loading_hint")}
	case consentScreen:
		return consentLines(m.snapshot, m.now)
	case keyScreen:
		return []string{i18n.Text(m.snapshot.Language, "worker.tui.key_step"), i18n.Text(m.snapshot.Language, "worker.tui.key_hint"), "> " + strings.Repeat("•", min(utf8.RuneCount(m.input), max(m.contentWidth()-2, 1))), i18n.Text(m.snapshot.Language, "worker.tui.key_actions")}
	default:
		if m.help {
			return []string{i18n.Text(m.snapshot.Language, "worker.tui.help_tasks"), i18n.Text(m.snapshot.Language, "worker.tui.help_scroll"), i18n.Text(m.snapshot.Language, "worker.tui.help_end"), i18n.Text(m.snapshot.Language, "worker.tui.help_back")}
		}
		return m.dashboard()
	}
}

func (m *model) taskLimit() int {
	if m.history {
		return min(len(m.snapshot.Tasks), 100)
	}
	return min(len(m.snapshot.Tasks), 20)
}
func (m *model) visibleTasks() []worker.LocalTaskView {
	return worker.RecentTaskViews(m.snapshot.Tasks, m.taskLimit())
}
func (m *model) selectedTask() (worker.LocalTaskView, bool) {
	for _, v := range m.visibleTasks() {
		if v.Task.ID == m.selected {
			return v, true
		}
	}
	return worker.LocalTaskView{}, false
}
func (m *model) ensureSelection() {
	if _, ok := m.selectedTask(); ok {
		return
	}
	if v, ok := client.PrimaryTask(m.visibleTasks()); ok {
		m.selected = v.Task.ID
	} else {
		m.selected = ""
	}
}
func (m *model) navigate(delta int) {
	n := m.taskLimit()
	if n == 0 {
		return
	}
	index := 0
	tasks := m.visibleTasks()
	for i, v := range tasks {
		if v.Task.ID == m.selected {
			index = i
			break
		}
	}
	m.selected = tasks[max(0, min(index+delta, n-1))].Task.ID
	m.scroll = 0
}

func (m *model) dashboard() []string {
	s := m.snapshot
	lines := []string{client.StateText(s.State, s.Language) + i18n.Text(m.snapshot.Language, "worker.tui.controller") + client.SafeText(s.Controller)}
	if s.State == "connecting" {
		lines = append(lines, m.activity()+i18n.Text(m.snapshot.Language, "worker.tui.connecting_elapsed")+max(m.now.Sub(m.connectingAt), 0).Round(time.Second).String())
	}
	if !s.ExpiresAt.IsZero() {
		lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.authorization_remaining")+remaining(s.ExpiresAt, m.now, s.Language))
	}
	if s.Notice != "" {
		lines = append(lines, client.SafeText(s.Notice))
	}
	primary, hasPrimary := client.PrimaryTask(s.Tasks)
	if hasPrimary {
		lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.current_section"), i18n.Text(m.snapshot.Language, "worker.tui.current_task")+client.TaskTitle(primary, s.Language))
		if primary.Task.ID != m.selected {
			lines = append(lines, m.graphicalProgress(primary), i18n.Text(m.snapshot.Language, "worker.tui.elapsed")+taskElapsed(primary, m.now, s.Language), stageRow(primary, s.Language))
		}
	}
	if v, ok := m.selectedTask(); ok {
		if !hasPrimary || v.Task.ID != primary.Task.ID {
			lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.selected_section"))
		}
		lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.selected_task")+client.TaskTitle(v, s.Language), m.graphicalProgress(v), i18n.Text(m.snapshot.Language, "worker.tui.elapsed")+taskElapsed(v, m.now, s.Language), stageRow(v, s.Language))
		if m.logs {
			lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.logs_section"))
			if len(v.Logs) == 0 {
				lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.logs_empty"))
			}
			// Bound the panel; snapshots already cap each task's collected logs.
			for _, log := range v.Logs[max(0, len(v.Logs)-100):] {
				lines = append(lines, client.SafeText(log.Message))
			}
		} else {
			lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.logs_collapsed"))
		}
	}
	lines = append(lines, i18n.Text(m.snapshot.Language, "worker.tui.recent", m.taskLimit(), min(len(s.Tasks), 100)))
	for i, v := range m.visibleTasks() {
		marker := "  "
		if v.Task.ID == m.selected {
			marker = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%d. %s · %s", marker, i+1, client.TaskTitle(v, s.Language), client.TaskResultText(v, s.Language)))
	}
	return lines
}

func (m *model) activity() string {
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
}
func (m *model) graphicalProgress(v worker.LocalTaskView) string {
	text := taskProgress(v, m.snapshot.Language)
	if v.Task.State.Terminal() {
		return text
	}
	if v.Progress.Stage == "downloading" && v.Progress.TotalBytes > 0 {
		const cells = 24
		completed := max(0, min(v.Progress.CompletedBytes, v.Progress.TotalBytes))
		filled := int(float64(completed) / float64(v.Progress.TotalBytes) * cells)
		return "[" + strings.Repeat("█", filled) + strings.Repeat("░", cells-filled) + "] " + text
	}
	return m.activity() + " " + text
}
func stageRow(v worker.LocalTaskView, language ...i18n.Language) string {
	lang := displayLanguage(language)
	stages := []string{"queued", "downloading", "verifying_artifact", "awaiting_uac", "installing", "verifying_version", "succeeded"}
	labels := []string{i18n.Text(lang, "worker.tui.stage_queued"), i18n.Text(lang, "worker.tui.stage_download"), i18n.Text(lang, "worker.tui.stage_verify"), i18n.Text(lang, "worker.tui.stage_uac"), i18n.Text(lang, "worker.tui.stage_install"), i18n.Text(lang, "worker.tui.stage_version"), i18n.Text(lang, "worker.tui.stage_done")}
	if v.Task.Spec.Type == "inspect_system" {
		stages = []string{"queued", "inspecting", "succeeded"}
		labels = []string{i18n.Text(lang, "worker.tui.stage_queued"), i18n.Text(lang, "worker.tui.stage_inspect"), i18n.Text(lang, "worker.tui.stage_done")}
	}
	current := -1
	stage := v.Progress.Stage
	if v.Task.State.Terminal() {
		stage = string(v.Task.State)
	}
	for i, s := range stages {
		if s == stage {
			current = i
			break
		}
	}
	for i, label := range labels {
		marker := "○"
		if i < current {
			marker = "✓"
		} else if i == current {
			marker = "●"
		}
		labels[i] = marker + label
	}
	return i18n.Text(lang, "worker.tui.stage") + strings.Join(labels, " → ")
}

func taskProgress(v worker.LocalTaskView, language ...i18n.Language) string {
	return client.TaskResultText(v, language...)
}
func taskElapsed(v worker.LocalTaskView, now time.Time, language ...i18n.Language) string {
	lang := displayLanguage(language)
	if v.Task.StartedAt == nil {
		return i18n.Text(lang, "worker.tui.not_started")
	}
	end := now
	if v.Task.FinishedAt != nil {
		end = *v.Task.FinishedAt
	}
	return max(end.Sub(*v.Task.StartedAt), 0).Round(time.Second).String()
}
func (m *model) contentWidth() int { return max(m.width-4, 1) }
func (m *model) bodyHeight() int   { return max(m.height-6, 1) }
func (m *model) wrapped() []string {
	return strings.Split(ansi.Hardwrap(strings.Join(m.body(), "\n"), m.contentWidth(), false), "\n")
}
func (m *model) maxScroll() int { return max(len(m.wrapped())-m.bodyHeight(), 0) }

func (m *model) View() string {
	lines := m.wrapped()
	offset := min(max(m.scroll, 0), max(len(lines)-m.bodyHeight(), 0))
	end := min(offset+m.bodyHeight(), len(lines))
	header := i18n.Text(m.snapshot.Language, "worker.tui.header")
	footer := i18n.Text(m.snapshot.Language, "worker.tui.footer_exit")
	switch m.screen {
	case pathScreen:
		footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_path")
	case consentScreen:
		if offset < m.maxScroll() {
			footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_review")
		} else {
			footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_consent")
		}
	case keyScreen:
		footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_key")
	case dashboardScreen:
		footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_dashboard")
		if m.snapshot.State == "disconnected" || m.snapshot.State == "error" {
			footer = i18n.Text(m.snapshot.Language, "worker.tui.footer_ended")
		}
	}
	if m.confirmDisconnect {
		footer = i18n.Text(m.snapshot.Language, "worker.tui.confirm_disconnect")
	}
	if m.languageMenu {
		footer = i18n.Text(m.snapshot.Language, "worker.tui.language_footer")
	}
	status := m.err
	if status == "" {
		status = i18n.Text(m.snapshot.Language, "worker.tui.scroll_status", offset+1, end, len(lines))
	}
	styles := m.styles()
	content := []string{styles.header.Width(m.contentWidth()).Render(ansi.Truncate(header, m.contentWidth(), "")), styles.section.Render(strings.Repeat("─", m.contentWidth()))}
	for _, line := range lines[offset:end] {
		content = append(content, styles.paint(line))
	}
	for len(content) < m.bodyHeight()+2 {
		content = append(content, "")
	}
	card := styles.card.Width(max(m.width-2, 1)).Render(strings.Join(content, "\n"))
	result := card + "\n" + styles.paint(ansi.Truncate(client.SafeText(status), m.width, "…")) + "\n" + styles.muted.Render(ansi.Truncate(footer, m.width, "…"))
	rows := strings.Split(result, "\n")
	if len(rows) > m.height {
		rows = rows[:m.height]
	}
	return strings.Join(rows, "\n")
}

type terminalStyles struct{ header, section, card, muted, warning, danger, success lipgloss.Style }

func (m *model) styles() terminalStyles {
	r := m.renderer
	s := terminalStyles{header: r.NewStyle(), section: r.NewStyle(), card: r.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1), muted: r.NewStyle(), warning: r.NewStyle(), danger: r.NewStyle(), success: r.NewStyle()}
	_, noColor := os.LookupEnv("NO_COLOR")
	if !noColor && !strings.EqualFold(os.Getenv("TERM"), "dumb") {
		s.header = s.header.Foreground(lipgloss.Color("#E6FFFA")).Background(lipgloss.Color("#102A43")).Bold(true)
		s.section = s.section.Foreground(lipgloss.Color("#2DD4BF")).Bold(true)
		s.card = s.card.BorderForeground(lipgloss.Color("#2DD4BF"))
		s.muted = s.muted.Foreground(lipgloss.Color("#8AA6BC"))
		s.warning = s.warning.Foreground(lipgloss.Color("#FBBF24"))
		s.danger = s.danger.Foreground(lipgloss.Color("#FB7185"))
		s.success = s.success.Foreground(lipgloss.Color("#2DD4BF"))
	}
	return s
}
func (s terminalStyles) paint(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(line, "失败") || strings.Contains(line, "无效") || strings.Contains(line, "无法") || strings.Contains(lower, "failed") || strings.Contains(lower, "invalid") || strings.Contains(lower, "cannot"):
		return s.danger.Render(line)
	case strings.Contains(line, "权限") || strings.Contains(line, "断开") || strings.Contains(line, "取消") || strings.Contains(lower, "permission") || strings.Contains(lower, "disconnect") || strings.Contains(lower, "cancel"):
		return s.warning.Render(line)
	case strings.HasPrefix(line, "──") || strings.HasPrefix(line, "选中任务") || strings.HasPrefix(line, "Selected task:") || strings.Contains(line, "█"):
		return s.section.Render(line)
	case strings.Contains(line, "已连接") || strings.Contains(line, "已完成") || strings.Contains(line, "版本已验证") || strings.Contains(line, "Connected") || strings.Contains(lower, "completed") || strings.Contains(lower, "version verified"):
		return s.success.Render(line)
	default:
		return line
	}
}
