package tui

import (
	"hash/fnv"
	"image/color"

	"charm.land/lipgloss/v2"
)

type styles struct {
	dark bool

	header, headerDim, title                                          lipgloss.Style
	sidebar, roomItem, roomFocused, roomUnread, roomMention           lipgloss.Style
	time, text, self, system, action, meerkat, errorText, okText, dim lipgloss.Style
	divider, footer, art                                              lipgloss.Style
	nickColors                                                        []color.Color
}

func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	fg := ld(lipgloss.Color("#1F2328"), lipgloss.Color("#E6E6E6"))
	muted := ld(lipgloss.Color("#6E7781"), lipgloss.Color("#7D8590"))
	faint := ld(lipgloss.Color("#AFB8C1"), lipgloss.Color("#3D444D"))
	accent := ld(lipgloss.Color("#9A6700"), lipgloss.Color("#E3B341")) // meerkat sand
	amber := ld(lipgloss.Color("#9A6700"), lipgloss.Color("#D4A15A"))
	red := ld(lipgloss.Color("#CF222E"), lipgloss.Color("#FF7B72"))
	green := ld(lipgloss.Color("#1A7F37"), lipgloss.Color("#7EE787"))
	barBG := ld(lipgloss.Color("#F0EBE3"), lipgloss.Color("#1C1A17"))

	s := styles{dark: dark}
	s.header = lipgloss.NewStyle().Background(barBG).Foreground(fg).Padding(0, 1)
	s.headerDim = lipgloss.NewStyle().Background(barBG).Foreground(muted)
	s.title = lipgloss.NewStyle().Background(barBG).Foreground(accent).Bold(true)
	s.sidebar = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false).BorderForeground(faint).PaddingRight(1)
	s.roomItem = lipgloss.NewStyle().Foreground(muted)
	s.roomFocused = lipgloss.NewStyle().Foreground(accent).Bold(true)
	s.roomUnread = lipgloss.NewStyle().Foreground(fg).Bold(true)
	s.roomMention = lipgloss.NewStyle().Foreground(red).Bold(true)
	s.time = lipgloss.NewStyle().Foreground(faint)
	s.text = lipgloss.NewStyle().Foreground(fg)
	s.self = lipgloss.NewStyle().Foreground(muted)
	s.system = lipgloss.NewStyle().Foreground(muted)
	s.action = lipgloss.NewStyle().Foreground(fg).Italic(true)
	s.meerkat = lipgloss.NewStyle().Foreground(amber).Italic(true)
	s.errorText = lipgloss.NewStyle().Foreground(red)
	s.okText = lipgloss.NewStyle().Foreground(green)
	s.dim = lipgloss.NewStyle().Foreground(muted)
	s.divider = lipgloss.NewStyle().Foreground(faint)
	s.footer = lipgloss.NewStyle().Foreground(muted)
	s.art = lipgloss.NewStyle().Foreground(amber)
	if dark {
		s.nickColors = hexes("#FF7B72", "#7EE787", "#E3B341", "#79C0FF", "#D2A8FF", "#56D4DD", "#FFA657", "#FF9BCE")
	} else {
		s.nickColors = hexes("#CF222E", "#1A7F37", "#9A6700", "#0969DA", "#8250DF", "#1B7C83", "#BC4C00", "#BF3989")
	}
	return s
}

func hexes(h ...string) []color.Color {
	out := make([]color.Color, len(h))
	for i, c := range h {
		out[i] = lipgloss.Color(c)
	}
	return out
}

// nick colors a nickname consistently: the same name always gets the same
// color, on every screen.
func (s styles) nick(name string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return lipgloss.NewStyle().Foreground(s.nickColors[h.Sum32()%uint32(len(s.nickColors))]).Bold(true)
}
