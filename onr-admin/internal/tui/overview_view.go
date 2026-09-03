package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type overviewMsg struct{ snapshot overviewSnapshot }

func overviewCmd(s *adminService) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return overviewMsg{snapshot: overviewSnapshot{RefreshedAt: time.Now()}}
		}
		return overviewMsg{snapshot: s.Snapshot(context.Background())}
	}
}

func renderOverview(snapshot overviewSnapshot, width int) string {
	if width < 40 {
		width = 40
	}
	cardWidth := (width - 5) / 2
	if cardWidth < 28 {
		cardWidth = 28
	}
	redisStatus := "disabled"
	if snapshot.RedisEnabled {
		redisStatus = "unavailable"
		if snapshot.RedisReachable {
			redisStatus = "reachable"
		}
	}
	redis := card("Redis", cardWidth,
		fmt.Sprintf("status       %s", redisStatus),
		fmt.Sprintf("key prefix   %s", valueOrDash(snapshot.KeyPrefix)),
		fmt.Sprintf("access mode  %s", valueOrDash(snapshot.AccessKeyMode)),
		optionalError(snapshot.RedisError),
	)
	billingState := "disabled"
	if snapshot.BillingEnabled {
		billingState = "enabled"
	}
	billing := card("Local billing", cardWidth,
		fmt.Sprintf("status       %s", billingState),
		fmt.Sprintf("currency     %s", valueOrDash(snapshot.Currency)),
		fmt.Sprintf("ledger       Redis"),
	)
	stream := card("Billing stream", cardWidth,
		fmt.Sprintf("pending      %d", snapshot.Pending),
		fmt.Sprintf("dead-letter  %d", snapshot.DeadLetter),
		fmt.Sprintf("group        %s", valueOrDash(snapshot.ConsumerGroup)),
		fmt.Sprintf("consumer     %s", valueOrDash(snapshot.ConsumerName)),
		fmt.Sprintf("max attempts %d", snapshot.MaxAttempts),
		optionalError(snapshot.BillingError),
	)
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, redis, billing),
		stream,
		lipgloss.NewStyle().Faint(true).Render("Last refreshed: "+snapshot.RefreshedAt.Format(time.RFC3339)),
	)
}

func card(title string, width int, lines ...string) string {
	content := []string{lipgloss.NewStyle().Bold(true).Render(title)}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			content = append(content, line)
		}
	}
	return lipgloss.NewStyle().Width(width).Padding(0, 1).Border(lipgloss.RoundedBorder()).Render(strings.Join(content, "\n"))
}

func optionalError(err string) string {
	if strings.TrimSpace(err) == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("error: " + err)
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
