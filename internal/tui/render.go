package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/identity"
)

const (
	timeWidth = 5 // "15:04"
	nickWidth = 16
)

// renderEvent formats one event as one or more lines of width w. It returns
// "" for events that should not appear in the transcript.
func (m *Model) renderEvent(e chat.Event, w int) string {
	s := m.st
	ts := s.time.Render(e.Time.Local().Format("15:04"))
	switch {
	case e.Pending:
		ts = s.time.Render("  …  ") // sent, not yet confirmed by the relay
	case e.Failed:
		ts = s.errorText.Render("  ✗  ")
		e.Text += " (not delivered)"
	}
	nick := chat.Sanitize(e.Nick, false)
	text := chat.Sanitize(e.Text, true)

	gutter := func(label string, st lipgloss.Style) string {
		label = truncate(label, nickWidth)
		pad := strings.Repeat(" ", max(0, nickWidth-lipgloss.Width(label)))
		return pad + st.Render(label)
	}
	body := func(prefix, msg string, st lipgloss.Style) string {
		indent := timeWidth + 1 + nickWidth + 3
		avail := max(10, w-indent)
		lines := strings.Split(lipgloss.Wrap(msg, avail, " "), "\n")
		for i := range lines {
			lines[i] = st.Render(lines[i])
		}
		return ts + " " + prefix + s.divider.Render(" │ ") + strings.Join(lines, "\n"+strings.Repeat(" ", indent))
	}

	switch e.Kind {
	case chat.KindMessage:
		st := s.text
		if e.Self {
			st = s.self
		}
		if e.Trust == identity.TrustChanged {
			return body(gutter("⚠ "+nick, s.errorText.Bold(true)), text, st)
		}
		return body(gutter(nick, s.nick(nick)), text, st)
	case chat.KindAction:
		if e.Trust == identity.TrustChanged {
			nick = "⚠ " + nick
		}
		return body(gutter("*", s.dim), nick+" "+text, s.action)
	case chat.KindMeerkat:
		return body(gutter("🐾 "+nick, s.meerkat), text, s.meerkat)
	case chat.KindJoin:
		return body(gutter("→", s.okText), nick+" joined", s.system)
	case chat.KindLeave:
		return body(gutter("←", s.dim), nick+" left", s.system)
	case chat.KindNick:
		return body(gutter("~", s.dim), fmt.Sprintf("%s is now known as %s", chat.Sanitize(e.Text, false), nick), s.system)
	case chat.KindSystem:
		return body(gutter("─", s.dim), text, s.system)
	case chat.KindStatus:
		switch e.Status {
		case chat.StatusOffline:
			msg := "connection lost, reconnecting…"
			if text != "" {
				msg = "connection lost (" + text + "), reconnecting…"
			}
			return body(gutter("!", s.errorText), msg, s.errorText)
		case chat.StatusOnline:
			return body(gutter("✓", s.okText), "reconnected", s.okText)
		}
	}
	return ""
}

func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
