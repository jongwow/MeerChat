// Package tui is MeerChat's terminal interface, built on Bubble Tea.
package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/identity"
	"github.com/jongwow/meerchat/internal/meerkat"
	"github.com/jongwow/meerchat/internal/version"
)

// Options configure the TUI.
type Options struct {
	// Title is shown in the header.
	Title string
	// DetachHint explains what leaving the TUI does.
	DetachHint string
	// Shutdown, if set, enables /quit, which stops the backend.
	Shutdown func(context.Context) error
	// AutoJoin, if set, is joined on start (used by the SSH demo lobby).
	AutoJoin, AutoJoinPassword string
	// Banner is shown on the welcome screen and as the first notice.
	Banner string
}

// Run starts the TUI and blocks until the user leaves.
func Run(ctx context.Context, be chat.Backend, opts Options, progOpts ...tea.ProgramOption) error {
	m := New(ctx, be, opts)
	p := tea.NewProgram(m, append([]tea.ProgramOption{tea.WithContext(ctx)}, progOpts...)...)
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil //nolint:nilerr // killed because our context ended: a normal exit
	}
	return err
}

// Model is the Bubble Tea model.
type Model struct {
	ctx  context.Context
	be   chat.Backend
	opts Options
	st   styles

	width, height int

	rooms []*roomState
	focus int
	nick  string

	input       textinput.Model
	vp          viewport.Model
	history     []string
	historyPos  int
	pendingJoin string // address awaiting a password
	busy        string

	notice    string
	noticeErr bool
	noticeAt  time.Time

	events <-chan chat.Event

	mentionFor string
	mentionRE  *regexp.Regexp

	// warned holds room+nick+key combinations already called out, so a
	// key change produces one explanation, not one per message.
	warned map[string]bool
}

type roomState struct {
	info    chat.RoomInfo
	events  []chat.Event
	unread  int
	mention bool
	// offline is set after a disconnect, so the next "online" can be shown
	// as a reconnect. Initial connects (including ones replayed from the
	// daemon's backlog) are not news.
	offline bool
}

const backlog = 300

// New returns a Model.
func New(ctx context.Context, be chat.Backend, opts Options) *Model {
	if opts.Title == "" {
		opts.Title = "meer"
	}
	if opts.DetachHint == "" {
		opts.DetachHint = "detach"
	}
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "say something, or /help"
	in.CharLimit = 4000
	in.Focus()
	m := &Model{ctx: ctx, be: be, opts: opts, st: newStyles(true), input: in, vp: viewport.New()}
	m.input.SetStyles(textinput.DefaultStyles(true))
	return m
}

// Messages.
type (
	subscribedMsg struct{ ch <-chan chat.Event }
	eventMsg      chat.Event
	subClosedMsg  struct{}
	roomsMsg      []chat.RoomInfo
	nickMsg       string
	joinedMsg     chat.RoomInfo
	noticeMsg     struct {
		text string
		err  bool
	}
	clearNoticeMsg struct{ at time.Time }
)

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.subscribe(), m.loadRooms(), m.loadNick(), tea.RequestBackgroundColor}
	if m.opts.AutoJoin != "" {
		cmds = append(cmds, m.join(m.opts.AutoJoin, m.opts.AutoJoinPassword))
	}
	if m.opts.Banner != "" {
		cmds = append(cmds, m.setNotice(m.opts.Banner, false))
	}
	return tea.Batch(cmds...)
}

func (m *Model) subscribe() tea.Cmd {
	return func() tea.Msg {
		ch, err := m.be.Subscribe(m.ctx, backlog)
		if err != nil {
			return noticeMsg{"subscribe: " + err.Error(), true}
		}
		return subscribedMsg{ch}
	}
}

func waitEvent(ch <-chan chat.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return subClosedMsg{}
		}
		return eventMsg(e)
	}
}

func (m *Model) loadRooms() tea.Cmd {
	return func() tea.Msg {
		rs, err := m.be.Rooms(m.ctx)
		if err != nil {
			return noticeMsg{err.Error(), true}
		}
		return roomsMsg(rs)
	}
}

func (m *Model) loadNick() tea.Cmd {
	return func() tea.Msg {
		n, err := m.be.Nick(m.ctx)
		if err != nil {
			return noticeMsg{err.Error(), true}
		}
		return nickMsg(n)
	}
}

func (m *Model) join(address, password string) tea.Cmd {
	m.busy = "deriving key and connecting to " + address + " …"
	return func() tea.Msg {
		info, err := m.be.Join(m.ctx, address, password)
		if err != nil {
			return noticeMsg{"join: " + err.Error(), true}
		}
		return joinedMsg(info)
	}
}

func (m *Model) do(f func() error, ok string) tea.Cmd {
	return func() tea.Msg {
		if err := f(); err != nil {
			return noticeMsg{err.Error(), true}
		}
		if ok != "" {
			return noticeMsg{ok, false}
		}
		return nil
	}
}

func (m *Model) setNotice(text string, isErr bool) tea.Cmd {
	m.notice, m.noticeErr, m.noticeAt = text, isErr, time.Now()
	at := m.noticeAt
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearNoticeMsg{at} })
}

func (m *Model) room(url string) *roomState {
	for _, r := range m.rooms {
		if r.info.URL == url {
			return r
		}
	}
	return nil
}

func (m *Model) focused() *roomState {
	if m.focus >= 0 && m.focus < len(m.rooms) {
		return m.rooms[m.focus]
	}
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()

	case tea.BackgroundColorMsg:
		m.st = newStyles(msg.IsDark())
		m.input.SetStyles(textinput.DefaultStyles(msg.IsDark()))
		m.refresh(false)

	case subscribedMsg:
		m.events = msg.ch
		cmds = append(cmds, waitEvent(m.events))

	case subClosedMsg:
		// We fell behind or the daemon went away. Start over from backlog.
		for _, r := range m.rooms {
			r.events = nil
		}
		cmds = append(cmds, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return resubscribeMsg{} }))

	case resubscribeMsg:
		cmds = append(cmds, m.subscribe(), m.loadRooms())

	case eventMsg:
		cmds = append(cmds, m.handleEvent(chat.Event(msg)), waitEvent(m.events))

	case roomsMsg:
		m.mergeRooms(msg)

	case replaceRoomsMsg:
		keep := make(map[string]bool, len(msg))
		for _, info := range msg {
			keep[info.URL] = true
		}
		var rooms []*roomState
		for _, r := range m.rooms {
			if keep[r.info.URL] {
				rooms = append(rooms, r)
			}
		}
		m.rooms = rooms
		m.setFocus(min(m.focus, len(m.rooms)-1))
		m.mergeRooms(msg)

	case nickMsg:
		m.nick = string(msg)

	case joinedMsg:
		m.busy = ""
		m.mergeRooms([]chat.RoomInfo{chat.RoomInfo(msg)})
		for i, r := range m.rooms {
			if r.info.URL == msg.URL {
				m.setFocus(i)
			}
		}
		cmds = append(cmds, m.setNotice(fmt.Sprintf("joined %s · key fingerprint %s — compare it with the people you're talking to", msg.URL, msg.Fingerprint), false))

	case noticeMsg:
		m.busy = ""
		cmds = append(cmds, m.setNotice(msg.text, msg.err))

	case clearNoticeMsg:
		if msg.at.Equal(m.noticeAt) {
			m.notice = ""
		}

	case tea.KeyPressMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

type resubscribeMsg struct{}

func (m *Model) mergeRooms(infos []chat.RoomInfo) {
	for _, info := range infos {
		if r := m.room(info.URL); r != nil {
			r.info = info
			continue
		}
		m.rooms = append(m.rooms, &roomState{info: info})
	}
	if m.focused() == nil && len(m.rooms) > 0 {
		m.setFocus(len(m.rooms) - 1)
	}
	m.layout() // the sidebar may have appeared
}

func (m *Model) handleEvent(e chat.Event) tea.Cmd {
	r := m.room(e.Room)
	if r == nil {
		if e.Kind == chat.KindSystem && strings.HasPrefix(e.Text, "left ") {
			return m.setNotice(e.Text, false)
		}
		// An event for a room we have not listed yet: the daemon knows
		// about it, so fetch the list again.
		addr, err := chat.ParseAddress(e.Room)
		if err != nil {
			return nil
		}
		r = &roomState{info: chat.RoomInfo{URL: e.Room, Name: addr.Room, Server: addr.Host}}
		m.rooms = append(m.rooms, r)
		if len(m.rooms) == 1 {
			m.setFocus(0)
		}
		defer m.layout()
	}

	switch e.Kind {
	case chat.KindPeers:
		r.info.Peers = e.Peers
		return nil
	case chat.KindStatus:
		r.info.Status = e.Status
		switch {
		case e.Status == chat.StatusOffline && !r.offline:
			r.offline = true
		case e.Status == chat.StatusOnline && r.offline:
			r.offline = false
		default:
			return nil
		}
	}
	if e.ID != "" && !e.Pending && replacePending(r, e) {
		// Our own message, confirmed (or failed) in place.
		if r == m.focused() {
			m.refresh(false)
		}
		return nil
	}
	r.events = append(r.events, e)
	if e.Trust == identity.TrustChanged {
		k := e.Room + "\x00" + e.Nick + "\x00" + e.Key
		if !m.warned[k] {
			if m.warned == nil {
				m.warned = map[string]bool{}
			}
			m.warned[k] = true
			nick := chat.Sanitize(e.Nick, false)
			r.events = append(r.events, chat.Event{Room: e.Room, Kind: chat.KindSystem, Time: e.Time, Text: fmt.Sprintf(
				"⚠ %s is speaking with a different key (%s) than before. a new device, or someone else using the name? check with them, then /trust %s",
				nick, e.Key, nick)})
		}
	}
	if over := len(r.events) - 2*backlog; over > 0 {
		r.events = append(r.events[:0:0], r.events[over:]...)
	}

	var cmd tea.Cmd
	if r != m.focused() && !e.History && isChat(e) {
		r.unread++
		if m.mentions(e) {
			r.mention = true
		}
	}
	if !e.History && !e.Self && m.mentions(e) {
		cmd = tea.Raw("\a")
	}
	if r == m.focused() {
		m.refresh(false)
	}
	return cmd
}

func isChat(e chat.Event) bool {
	return e.Kind == chat.KindMessage || e.Kind == chat.KindAction || e.Kind == chat.KindMeerkat
}

func (m *Model) mentions(e chat.Event) bool {
	if m.nick == "" || e.Self || (e.Kind != chat.KindMessage && e.Kind != chat.KindAction) {
		return false
	}
	if m.mentionRE == nil || m.mentionFor != m.nick {
		m.mentionFor = m.nick
		m.mentionRE = regexp.MustCompile(`(?i)(^|\W)@?` + regexp.QuoteMeta(m.nick) + `($|\W)`)
	}
	return m.mentionRE.MatchString(e.Text)
}

func (m *Model) setFocus(i int) {
	if len(m.rooms) == 0 {
		m.focus = 0
		return
	}
	m.focus = (i%len(m.rooms) + len(m.rooms)) % len(m.rooms)
	r := m.rooms[m.focus]
	r.unread, r.mention = 0, false
	m.refresh(true)
}

func (m *Model) handleKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "ctrl+c":
		if m.pendingJoin != "" || m.input.Value() != "" {
			m.cancelInput()
			return nil, true
		}
		return tea.Quit, true
	case "ctrl+d":
		return tea.Quit, true
	case "esc":
		m.cancelInput()
		return nil, true
	case "tab", "ctrl+n":
		m.setFocus(m.focus + 1)
		return nil, true
	case "shift+tab", "ctrl+p":
		m.setFocus(m.focus - 1)
		return nil, true
	case "pgup":
		m.vp.PageUp()
		return nil, true
	case "pgdown":
		m.vp.PageDown()
		return nil, true
	case "up":
		if m.pendingJoin == "" && m.historyPos > 0 {
			m.historyPos--
			m.input.SetValue(m.history[m.historyPos])
			m.input.CursorEnd()
		}
		return nil, true
	case "down":
		if m.pendingJoin == "" && m.historyPos < len(m.history) {
			m.historyPos++
			if m.historyPos == len(m.history) {
				m.input.SetValue("")
			} else {
				m.input.SetValue(m.history[m.historyPos])
			}
			m.input.CursorEnd()
		}
		return nil, true
	case "enter":
		return m.submit(), true
	}
	if s := k.String(); len(s) == 5 && strings.HasPrefix(s, "alt+") && s[4] >= '1' && s[4] <= '9' {
		if i := int(s[4] - '1'); i < len(m.rooms) {
			m.setFocus(i)
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) cancelInput() {
	m.pendingJoin = ""
	m.input.EchoMode = textinput.EchoNormal
	m.input.Prompt = "› "
	m.input.Placeholder = "say something, or /help"
	m.input.SetValue("")
	m.layout()
}

func (m *Model) submit() tea.Cmd {
	line := m.input.Value()
	if m.pendingJoin != "" {
		addr := m.pendingJoin
		m.cancelInput()
		return m.join(addr, line)
	}
	m.input.SetValue("")
	if strings.TrimSpace(line) == "" {
		return nil
	}
	m.history = append(m.history, line)
	m.historyPos = len(m.history)

	if strings.HasPrefix(line, "//") {
		line = line[1:] // escape a leading slash
	} else if strings.HasPrefix(line, "/") {
		return m.command(line)
	}
	room := m.focused()
	if room == nil {
		return m.setNotice("you're not in a room yet. try /join meer://host/room", true)
	}
	url := room.info.URL
	return m.do(func() error { return m.be.Send(m.ctx, url, chat.KindMessage, line) }, "")
}

func (m *Model) command(line string) tea.Cmd {
	cmd, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	arg = strings.TrimSpace(arg)
	room := m.focused()
	switch cmd {
	case "join", "j":
		if arg == "" {
			return m.setNotice("usage: /join meer://host/room", true)
		}
		if _, err := chat.ParseAddress(arg); err != nil {
			return m.setNotice(err.Error(), true)
		}
		m.pendingJoin = arg
		m.input.EchoMode = textinput.EchoPassword
		m.input.EchoCharacter = '•'
		m.input.Prompt = "password › "
		m.input.Placeholder = "room password (esc to cancel)"
		m.layout()
		return nil
	case "leave", "part":
		target := arg
		if target == "" {
			if room == nil {
				return m.setNotice("not in a room", true)
			}
			target = room.info.URL
		}
		return func() tea.Msg {
			if err := m.be.Leave(m.ctx, target); err != nil {
				return noticeMsg{err.Error(), true}
			}
			rs, err := m.be.Rooms(m.ctx)
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			return replaceRoomsMsg(rs)
		}
	case "nick":
		if arg == "" {
			return m.setNotice("you are "+m.nick, false)
		}
		m.nick = arg
		return m.do(func() error { return m.be.SetNick(m.ctx, arg) }, "you are now "+arg)
	case "me":
		if room == nil || arg == "" {
			return m.setNotice("usage: /me waves", true)
		}
		url := room.info.URL
		return m.do(func() error { return m.be.Send(m.ctx, url, chat.KindAction, arg) }, "")
	case "clear":
		if room != nil {
			room.events = nil
			m.refresh(true)
		}
		return nil
	case "whois":
		if arg == "" {
			return func() tea.Msg {
				key, err := m.be.Identity(m.ctx)
				if err != nil {
					return noticeMsg{err.Error(), true}
				}
				return noticeMsg{fmt.Sprintf("you are %s · device key %s", m.nick, key), false}
			}
		}
		if room == nil {
			return m.setNotice("not in a room", true)
		}
		for i := len(room.events) - 1; i >= 0; i-- {
			if e := room.events[i]; e.Nick == arg && e.Key != "" {
				return m.setNotice(fmt.Sprintf("%s · key %s · %s", arg, e.Key, trustWords(e.Trust)), e.Trust == identity.TrustChanged)
			}
		}
		return m.setNotice("haven't seen "+arg+" say anything in this room", true)
	case "trust":
		if room == nil || arg == "" {
			return m.setNotice("usage: /trust nick (after a key-change warning)", true)
		}
		url := room.info.URL
		return func() tea.Msg {
			fp, err := m.be.Trust(m.ctx, url, arg)
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			return noticeMsg{fmt.Sprintf("now trusting %s's key %s", arg, fp), false}
		}
	case "fp", "fingerprint":
		if room == nil {
			return m.setNotice("not in a room", true)
		}
		return m.setNotice(fmt.Sprintf("key fingerprint for %s: %s — everyone with the right password sees the same one", room.info.URL, room.info.Fingerprint), false)
	case "help", "?":
		return m.setNotice("/join addr · /leave · /nick name · /me action · /whois [nick] · /trust nick · /fp · /clear · tab: next room · pgup/pgdn: scroll · ctrl+d: "+m.opts.DetachHint+m.quitHelp(), false)
	case "detach":
		return tea.Quit
	case "quit", "exit":
		if m.opts.Shutdown == nil {
			return tea.Quit
		}
		return tea.Sequence(m.do(func() error { return m.opts.Shutdown(m.ctx) }, ""), tea.Quit)
	}
	return m.setNotice("unknown command /"+cmd+" (try /help, or // to send a literal slash)", true)
}

func (m *Model) quitHelp() string {
	if m.opts.Shutdown != nil {
		return " · /quit: leave all rooms and stop the daemon"
	}
	return ""
}

type replaceRoomsMsg []chat.RoomInfo

// layout recomputes component sizes.
func (m *Model) layout() {
	side := m.sidebarWidth()
	mainW := max(10, m.width-side)
	m.input.SetWidth(max(5, mainW-lipgloss.Width(m.input.Prompt)-2))
	m.vp.SetWidth(mainW)
	m.vp.SetHeight(max(1, m.height-3))
	m.refresh(false)
}

func (m *Model) sidebarWidth() int {
	if m.width < 60 || len(m.rooms) == 0 {
		return 0
	}
	w := len("rooms")
	for _, r := range m.rooms {
		w = max(w, lipgloss.Width(r.info.Name)+6) // marker, '#', badge
	}
	return min(w, 24, m.width/4) + 2
}

// refresh re-renders the focused room into the viewport.
func (m *Model) refresh(jump bool) {
	if m.width == 0 {
		return
	}
	atBottom := m.vp.AtBottom()
	w := m.vp.Width()
	r := m.focused()
	var content string
	if r == nil {
		content = m.welcome(w)
	} else {
		var b strings.Builder
		hasChat := false
		for _, e := range r.events {
			line := m.renderEvent(e, w)
			if line == "" {
				continue
			}
			hasChat = hasChat || isChat(e)
			b.WriteString(line)
			b.WriteByte('\n')
		}
		content = strings.TrimSuffix(b.String(), "\n")
		if !hasChat {
			content = m.st.art.Render(meerkat.Art) + "\n" + content
		}
	}
	m.vp.SetContent(content)
	if jump || atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) welcome(w int) string {
	s := m.st
	lines := []string{
		s.art.Render(meerkat.Art),
		s.text.Bold(true).Render("welcome to meer " + version.Version()),
		"",
		s.dim.Render("join a room:   ") + s.text.Render("/join meer://host/room"),
		s.dim.Render("everyone who types the same password can read along."),
		s.dim.Render("everyone who doesn't just hears meerkats."),
	}
	if m.opts.Banner != "" {
		lines = append(lines, "", s.errorText.Render(m.opts.Banner))
	}
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, strings.Join(lines, "\n"))
}

func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if m.width == 0 {
		v.SetContent("")
		return v
	}
	r := m.focused()
	if r != nil {
		v.WindowTitle = "meer · " + r.info.Name
	} else {
		v.WindowTitle = "meer"
	}

	header := m.headerView()
	main := m.vp.View()
	if side := m.sidebarWidth(); side > 0 {
		sb := m.st.sidebar.Width(side).Height(m.vp.Height()).Render(m.sidebarView(side - 2))
		main = lipgloss.JoinHorizontal(lipgloss.Top, sb, main)
	}
	footer := m.footerView()
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, header, main, m.input.View(), footer))
	return v
}

func (m *Model) headerView() string {
	s := m.st
	parts := []string{s.title.Render("🐾 " + m.opts.Title)}
	if r := m.focused(); r != nil {
		status := r.info.Status
		switch status {
		case chat.StatusOnline:
			status = s.headerDim.Render(fmt.Sprintf("%d here", r.info.Peers))
		default:
			status = s.errorText.Inherit(s.headerDim).Render(status)
		}
		parts = append(parts,
			s.headerDim.Render(r.info.URL),
			status,
			s.headerDim.Render("key "+r.info.Fingerprint),
		)
	}
	if m.nick != "" {
		parts = append(parts, s.headerDim.Render("as "+m.nick))
	}
	line := strings.Join(parts, s.headerDim.Render("  ·  "))
	return s.header.Width(m.width).MaxWidth(m.width).Render(truncate(line, m.width-2))
}

func (m *Model) sidebarView(w int) string {
	s := m.st
	var b strings.Builder
	b.WriteString(s.dim.Render("rooms") + "\n")
	for i, r := range m.rooms {
		name := "#" + r.info.Name
		badge := ""
		st := s.roomItem
		switch {
		case i == m.focus:
			st = s.roomFocused
			name = "▌" + name
		case r.mention:
			st = s.roomMention
			badge = " @"
		case r.unread > 0:
			st = s.roomUnread
			badge = fmt.Sprintf(" %d", r.unread)
		}
		if r.info.Status != chat.StatusOnline {
			badge += " ⚠"
		}
		if i != m.focus {
			name = " " + name
		}
		b.WriteString(st.Render(truncate(name, w-lipgloss.Width(badge))+badge) + "\n")
	}
	return b.String()
}

func (m *Model) footerView() string {
	s := m.st
	switch {
	case m.busy != "":
		return s.dim.Render("⏳ " + m.busy)
	case m.notice != "":
		st := s.okText
		if m.noticeErr {
			st = s.errorText
		}
		return st.Render(truncate(m.notice, m.width))
	}
	help := "enter send · tab next room · pgup/pgdn scroll · /help · ctrl+d " + m.opts.DetachHint
	if !m.vp.AtBottom() {
		help = "↓ more below · " + help
	}
	return s.footer.Render(truncate(help, m.width))
}

func trustWords(t string) string {
	switch t {
	case identity.TrustSelf:
		return "this is you"
	case identity.TrustNew, identity.TrustKnown:
		return "same key as when first seen here"
	case identity.TrustChanged:
		return "DIFFERENT key than when first seen here"
	}
	return t
}

// replacePending swaps the pending copy of e (same payload ID) for e. It
// keeps the message where it was typed rather than moving it.
func replacePending(r *roomState, e chat.Event) bool {
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].ID == e.ID && r.events[i].Pending {
			r.events[i] = e
			return true
		}
	}
	return false
}
