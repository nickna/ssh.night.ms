package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nickna/ssh.night.ms/internal/security/netlimit"
)

// Bans tab — active IP-ban list, plus the ban-ip / unban-ip commands.
// Lives alongside the Events tab so security-events triage and active-ban
// management are both one Tab away from the operator's landing pane.

// renderBans draws the Bans tab body — single-pane list of active IP bans.
func (m *Sysop) renderBans(w, h int) string {
	var b strings.Builder
	b.WriteString(sysopHeader.Render(fmt.Sprintf("active IP bans (%d):", len(m.bans))))
	b.WriteString("\n")

	if m.bansLoading && len(m.bans) == 0 {
		b.WriteString(sysopHint.Render("loading…"))
		return b.String()
	}

	rows := m.bans
	if len(rows) > h-2 {
		rows = rows[:h-2]
	}
	for _, ban := range rows {
		expires := "<none>"
		if ban.ExpiresAt.Valid {
			expires = sysopTSMin(ban.ExpiresAt.Time)
		}
		bannedAt := ""
		if ban.BannedAt.Valid {
			bannedAt = sysopTSMin(ban.BannedAt.Time)
		}
		reason := truncateRow(ban.Reason, 40)
		creator := truncateRow(ban.CreatedBy, 16)
		b.WriteString(fmt.Sprintf("  %-40s  expires %s  by %s\n",
			truncateRow(ban.IpAddr, 40),
			sysopMuted.Render(expires),
			sysopMuted.Render(creator),
		))
		b.WriteString(fmt.Sprintf("    %s · %s\n",
			sysopMuted.Render("set "+bannedAt),
			sysopMuted.Render(reason),
		))
	}
	if len(m.bans) == 0 {
		b.WriteString(sysopHint.Render("  (no active bans)") + "\n")
	}

	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// banIPCmd implements `ban-ip <ip> [duration] [reason]`. Duration parses
// via time.ParseDuration (e.g., "24h", "30m"); defaults to 24h when
// omitted. Reason is anything after the duration token; defaults to
// "sysop manual".
func (m *Sysop) banIPCmd(rest string) tea.Cmd {
	if rest == "" {
		return done("[!] ban-ip requires an IP. Usage: ban-ip <ip> [duration] [reason]", false)
	}
	if m.sess.Bans == nil {
		return done("[!] ban cache not configured.", false)
	}
	bans := m.sess.Bans
	actor := m.sess.Identity.Handle

	parts := strings.Fields(rest)
	rawIP := parts[0]
	key, err := netlimit.CollapseIPString(rawIP)
	if err != nil {
		return done("[!] ban-ip: "+rawIP+" is not a valid IP.", false)
	}

	duration := 24 * time.Hour
	reason := "sysop manual"
	if len(parts) >= 2 {
		if d, err := time.ParseDuration(parts[1]); err == nil {
			duration = d
			if len(parts) >= 3 {
				reason = strings.Join(parts[2:], " ")
			}
		} else {
			// Token 2 isn't a duration — treat the whole tail as the reason.
			reason = strings.Join(parts[1:], " ")
		}
	}

	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if err := bans.AddBan(ctx, key, duration, reason, actor); err != nil {
			return sysopCmdDoneMsg{status: "[!] ban-ip: " + err.Error()}
		}
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("ban-ip %s for %s — ok.", key, duration),
			reload: true,
		}
	}
}

// unbanIPCmd implements `unban-ip <ip>`. Normalizes the IP to the same
// collapsed-key form used by ban-ip so the canonical row gets removed
// even when the operator types a non-canonical input.
func (m *Sysop) unbanIPCmd(rest string) tea.Cmd {
	if rest == "" {
		return done("[!] unban-ip requires an IP. Usage: unban-ip <ip>", false)
	}
	if m.sess.Bans == nil {
		return done("[!] ban cache not configured.", false)
	}
	bans := m.sess.Bans
	actor := m.sess.Identity.Handle

	key, err := netlimit.CollapseIPString(rest)
	if err != nil {
		return done("[!] unban-ip: "+rest+" is not a valid IP.", false)
	}

	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		n, err := bans.RemoveBan(ctx, key, actor)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] unban-ip: " + err.Error()}
		}
		if n == 0 {
			return sysopCmdDoneMsg{status: "[!] unban-ip: no active ban for " + key}
		}
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("unban-ip %s — ok.", key),
			reload: true,
		}
	}
}
