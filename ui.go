package main

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	modeBrowse = iota
	modeConfirmRemove
	modeNaming
)

const (
	maxCardWidth = 50
	fetchGap     = 2 * time.Second
	cooldown     = 30 * time.Second
	backoff      = 5 * time.Minute
	indent       = "   "
)

var (
	cGreen  = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#87af87"}
	cRed    = lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#d78787"}
	cAccent = lipgloss.AdaptiveColor{Light: "#1f5fa8", Dark: "#87afd7"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6c6c6c", Dark: "#8a8a8a"}
	cFaint  = lipgloss.AdaptiveColor{Light: "#b2b2b2", Dark: "#585858"}

	sTitle  = lipgloss.NewStyle().Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sGreen  = lipgloss.NewStyle().Foreground(cGreen)
	sRed    = lipgloss.NewStyle().Foreground(cRed)
	sAccent = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sCard   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).
		Padding(0, 1)
	sCardSel = sCard.BorderForeground(cAccent)
)

type item struct {
	kind  string // "unsaved", "saved", "add"
	name  string
	email string
}

type usageMsg struct {
	name string
	u    *usage
	err  error
}

type kickMsg struct{}

type retryMsg struct{ name string }

type loginDoneMsg struct{ err error }

type model struct {
	items       []item
	active      string
	liveEmail   string
	cache       map[string]cachedUsage
	usageErr    map[string]string
	queue       []string
	inFlight    string
	lastFetch   time.Time
	cursor      int
	mode        int
	input       textinput.Model
	bar         progress.Model
	spin        spinner.Model
	status      string
	statusIsErr bool
	height      int
	width       int
	cardW       int
}

func newModel() model {
	bar := progress.New(progress.WithGradient("#87af87", "#d75f5f"), progress.WithoutPercentage(), progress.WithWidth(maxCardWidth-25))
	bar.Full, bar.Empty = '━', '─'
	bar.EmptyColor = "#d0d0d0"
	if lipgloss.HasDarkBackground() {
		bar.EmptyColor = "#4e4e4e"
	}
	in := textinput.New()
	in.Prompt = "name: "
	in.CharLimit = 32
	m := model{bar: bar, input: in, spin: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(sDim)),
		cache: loadCache(), usageErr: map[string]string{}, cardW: maxCardWidth}
	m.reload()
	m.cursorToActive()
	m.enqueueStale()
	return m
}

func (m *model) reload() {
	m.active = activeName()
	m.liveEmail = liveProfile().str("emailAddress")
	m.items = nil
	if m.liveEmail != "" && m.active == "" {
		m.items = append(m.items, item{kind: "unsaved", email: m.liveEmail})
	}
	all := names()
	for _, n := range all {
		m.items = append(m.items, item{kind: "saved", name: n, email: loadProfile(n).str("emailAddress")})
	}
	m.items = append(m.items, item{kind: "add"})
	m.queue = slices.DeleteFunc(m.queue, func(n string) bool { return !slices.Contains(all, n) })
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
}

func (m *model) cursorToActive() {
	for i, it := range m.items {
		if it.kind == "unsaved" || (it.name != "" && it.name == m.active) {
			m.cursor = i
			return
		}
	}
}

// enqueueStale queues every saved account whose wait since its last attempt is over; it reports how many were queued.
func (m *model) enqueueStale() int {
	added := 0
	for _, it := range m.items {
		n := it.name
		if it.kind != "saved" || n == m.inFlight || slices.Contains(m.queue, n) {
			continue
		}
		if m.coolingDown(n) {
			continue
		}
		m.queue = append(m.queue, n)
		added++
	}
	return added
}

// The usage endpoint rate-limits repeat checks of one account (about 3 per 30s trips it, and it shares that budget
// with Claude's own /usage), so each account is tried at most once per cooldown, or once per backoff after a 429.
// Counted from the last attempt and persisted, so reopening can't bypass it.
func (m model) wait(name string) time.Duration {
	if m.cache[name].Limited {
		return backoff
	}
	return cooldown
}

func (m model) coolingDown(name string) bool {
	return time.Since(m.cache[name].Tried) < m.wait(name)
}

func (m model) nextCheckIn(name string) time.Duration {
	return max(m.wait(name)-time.Since(m.cache[name].Tried), 0)
}

func kick() tea.Msg { return kickMsg{} }

// next starts at most one usage request, never sooner than fetchGap after the previous one.
func (m *model) next() tea.Cmd {
	if m.inFlight != "" || len(m.queue) == 0 {
		return nil
	}
	if wait := fetchGap - time.Since(m.lastFetch); wait > 0 {
		return tea.Tick(wait, func(time.Time) tea.Msg { return kickMsg{} })
	}
	name := m.queue[0]
	m.queue = m.queue[1:]
	m.inFlight, m.lastFetch = name, time.Now()
	live := name == m.active
	return func() tea.Msg {
		u, err := getUsage(name, live)
		return usageMsg{name, u, err}
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.spin.Tick, kick) }

func (m *model) setStatus(s string, isErr bool) { m.status, m.statusIsErr = s, isErr }

const saveFirst = "save the signed-in account first — press Enter on its card at the top"

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height, m.width = msg.Height, msg.Width
		m.cardW = max(min(maxCardWidth, msg.Width-2), 26)
		m.bar.Width = m.cardW - 25
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case kickMsg:
		return m, m.next()
	case retryMsg:
		m.enqueueStale()
		return m, m.next()
	case usageMsg:
		m.inFlight = ""
		c := m.cache[msg.name]
		c.Tried, c.Limited = time.Now(), false
		switch {
		case errors.Is(msg.err, errRateLimited):
			c.Limited = true
		case msg.err != nil:
			m.usageErr[msg.name] = msg.err.Error()
		default:
			delete(m.usageErr, msg.name)
			c.Usage, c.At = msg.u, c.Tried
		}
		m.cache[msg.name] = c
		saveCache(m.cache)
		if c.Limited {
			name := msg.name
			return m, tea.Batch(m.next(), tea.Tick(backoff, func(time.Time) tea.Msg { return retryMsg{name} }))
		}
		return m, m.next()
	case loginDoneMsg:
		m.reload()
		m.enqueueStale()
		if msg.err != nil {
			m.setStatus("sign-in cancelled — nothing changed. Press a to try again", true)
			return m, m.next()
		}
		if m.active != "" {
			m.cursorToActive()
			if err := saveLiveAs(m.active); err != nil {
				m.setStatus(err.Error(), true)
			} else {
				m.setStatus(fmt.Sprintf("'%s' was already saved — its sign-in is updated", m.active), false)
			}
			return m, m.next()
		}
		m.cursor = 0
		return m, tea.Batch(m.next(), m.startNaming())
	case tea.KeyMsg:
		switch m.mode {
		case modeNaming:
			return m.updateNaming(msg)
		case modeConfirmRemove:
			return m.updateConfirm(msg)
		}
		return m.updateBrowse(msg)
	}
	return m, nil
}

func (m *model) startNaming() tea.Cmd {
	m.mode = modeNaming
	m.input.SetValue(strings.Split(m.liveEmail, "@")[0])
	m.input.CursorEnd()
	m.setStatus("", false)
	return m.input.Focus()
}

func (m model) updateBrowse(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.setStatus("", false)
	key := k.String()
	if len(key) == 1 && key >= "1" && key <= "9" {
		want, seen := int(key[0]-'0'), 0
		for i, it := range m.items {
			if it.kind == "saved" {
				if seen++; seen == want {
					m.cursor = i
				}
			}
		}
		return m, nil
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "r":
		if m.enqueueStale() == 0 && m.inFlight == "" && len(m.queue) == 0 {
			wait := backoff
			for _, it := range m.items {
				if it.kind == "saved" {
					wait = min(wait, m.nextCheckIn(it.name))
				}
			}
			m.setStatus(fmt.Sprintf("all accounts checked recently — next check possible in %s (avoids rate limits)", fmtDur(wait)), false)
		}
		return m, m.next()
	case "a":
		return m.startLogin()
	case "d":
		if it := m.items[m.cursor]; it.kind == "saved" {
			if it.name == m.inFlight {
				m.setStatus("updating this account — try again in a moment", true)
				return m, nil
			}
			m.mode = modeConfirmRemove
		}
	case "enter":
		it := m.items[m.cursor]
		switch it.kind {
		case "unsaved":
			return m, m.startNaming()
		case "add":
			return m.startLogin()
		case "saved":
			if it.name == m.active {
				m.setStatus(fmt.Sprintf("already on '%s'", it.name), false)
				return m, nil
			}
			// An in-flight usage request may be renewing this account's tokens, which kills the old refresh token.
			if it.name == m.inFlight {
				m.setStatus("updating this account — try again in a moment", true)
				return m, nil
			}
			if err := use(it.name); err != nil {
				m.reload()
				if errors.Is(err, errUnsaved) {
					m.setStatus(saveFirst, true)
				} else {
					m.setStatus(err.Error(), true)
				}
				return m, nil
			}
			m.reload()
			m.cursorToActive()
			m.setStatus(fmt.Sprintf("switched to '%s' — new claude sessions use %s", it.name, it.email), false)
			return m, nil
		}
	}
	return m, nil
}

func (m model) startLogin() (tea.Model, tea.Cmd) {
	// The signed-in account may have changed outside this screen (e.g. /login inside Claude).
	if cur := activeName(); cur != m.active {
		m.reload()
		m.cursorToActive()
		m.setStatus("you signed in elsewhere meanwhile — screen refreshed, press a again", true)
		return m, nil
	}
	if m.active != "" {
		if err := saveLiveAs(m.active); err != nil {
			m.setStatus(err.Error(), true)
			return m, nil
		}
	} else if m.liveEmail != "" {
		m.setStatus(saveFirst, true)
		return m, nil
	}
	return m, tea.ExecProcess(exec.Command("claude", "auth", "login"), func(err error) tea.Msg { return loginDoneMsg{err} })
}

func (m model) updateConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeBrowse
	if k.String() == "y" {
		name := m.items[m.cursor].name
		remove(name)
		delete(m.cache, name)
		saveCache(m.cache)
		m.reload()
		m.setStatus(fmt.Sprintf("removed '%s'", name), false)
	}
	return m, nil
}

func (m model) updateNaming(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeBrowse
		m.input.Blur()
		m.setStatus("", false)
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.input.Value())
		if err := addCurrent(name); err != nil {
			m.setStatus(err.Error(), true)
			return m, nil
		}
		m.mode = modeBrowse
		m.input.Blur()
		m.reload()
		m.cursorToActive()
		m.enqueueStale()
		m.setStatus(fmt.Sprintf("saved %s as '%s'", m.liveEmail, name), false)
		return m, m.next()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func fmtReset(w *window, layout string) string {
	if w == nil || w.ResetsAt == nil {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, *w.ResetsAt)
	if err != nil {
		return ""
	}
	return strings.ToLower(t.Local().Format(layout))
}

func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func (m model) barLine(label string, w *window, layout string) string {
	if w == nil || w.Utilization == nil {
		return sDim.Render(label + "  —")
	}
	pct := *w.Utilization
	pctStyle := sGreen
	if pct >= 70 {
		pctStyle = sRed
	}
	return fmt.Sprintf("%s  %s  %s  %s", sDim.Render(label), m.bar.ViewAs(pct/100),
		pctStyle.Render(fmt.Sprintf("%3.0f%%", pct)), sDim.Render(fmtReset(w, layout)))
}

// row lays out plain left text (styled by style) and an already-styled right part on one card line,
// truncating the plain text before styling so colour codes are never cut.
func (m model) row(left string, style lipgloss.Style, right string) string {
	inner := m.cardW - 2
	left = truncate(left, inner-lipgloss.Width(right)-1)
	return style.Render(left) + strings.Repeat(" ", max(inner-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

func wrap(s string, width, maxLines int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && len([]rune(cur))+1+len([]rune(w)) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = truncate(lines[maxLines-1]+"…", width)
	}
	return lines
}

func (m model) cardStatus(name string) string {
	c, cached := m.cache[name]
	switch {
	case name == m.inFlight:
		return m.spin.View() + sDim.Render(" updating")
	case c.Limited:
		return sDim.Render("rate limited · retry in " + fmtDur(m.nextCheckIn(name)))
	case m.usageErr[name] != "":
		label := "update failed"
		if m.usageErr[name] == errOffline.Error() {
			label = "offline"
		}
		if cached && c.Usage != nil {
			return sRed.Render(label) + sDim.Render(" · "+fmtAge(c.At))
		}
		return sRed.Render(label)
	case slices.Contains(m.queue, name):
		if cached && c.Usage != nil {
			return sDim.Render(fmtAge(c.At) + " · queued")
		}
		return sDim.Render("queued")
	}
	if cached && c.Usage != nil {
		return sDim.Render(fmtAge(c.At))
	}
	return ""
}

func (m model) renderItem(i, number int) string {
	it, sel := m.items[i], i == m.cursor
	style := sCard.Width(m.cardW)
	if sel {
		style = sCardSel.Width(m.cardW)
	}
	switch it.kind {
	case "add":
		label := "+ add account"
		if sel {
			return " " + sAccent.Render("▸ "+label) + sDim.Render("  (opens browser sign-in)")
		}
		return "   " + sDim.Render(label)
	case "unsaved":
		head := m.row(it.email, sTitle, sRed.Render("not saved"))
		body := indent + sDim.Render("signed in now · press Enter to save it")
		if m.mode == modeNaming && sel {
			body = indent + m.input.View()
		}
		return style.Render(head + "\n" + body)
	}
	nameStyle := lipgloss.NewStyle()
	if sel {
		nameStyle = sTitle
	}
	num := sDim.Render(fmt.Sprintf("%-3d", number))
	mark := ""
	if it.name == m.active {
		mark = " " + sGreen.Render("✓")
	}
	room := m.cardW - 2 - 3 - lipgloss.Width(mark)
	head := num + nameStyle.Render(truncate(it.name, room)) + mark
	inner := m.cardW - 2
	head += strings.Repeat(" ", max(inner-lipgloss.Width(head), 0))
	lines := []string{head, indent + m.rowIndented(it.email, m.cardStatus(it.name))}
	reason := m.usageErr[it.name]
	if c, ok := m.cache[it.name]; ok && c.Usage != nil {
		lines = append(lines, indent+m.barLine("5h", c.Usage.FiveHour, "3:04pm"), indent+m.barLine("7d", c.Usage.SevenDay, "Mon 3pm"))
	} else if reason != "" {
		lines = append(lines, indent+sDim.Render("no usage yet"))
	} else if m.cache[it.name].Limited {
		lines = append(lines, indent+sDim.Render(truncate("no usage yet — waiting out the rate limit", inner-len(indent))), "")
	} else {
		lines = append(lines, indent+m.spin.View()+sDim.Render(" loading usage"), "")
	}
	if reason != "" {
		for _, l := range wrap(reason, inner-len(indent), 3) {
			lines = append(lines, indent+sRed.Render(l))
		}
	}
	return style.Render(strings.Join(lines, "\n"))
}

// rowIndented is row() for lines that start after the card's indent.
func (m model) rowIndented(left, right string) string {
	inner := m.cardW - 2 - len(indent)
	left = truncate(left, inner-lipgloss.Width(right)-1)
	return sDim.Render(left) + strings.Repeat(" ", max(inner-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right
}

func (m model) help() string {
	if m.mode == modeConfirmRemove {
		it := m.items[m.cursor]
		if it.name == m.active {
			return sRed.Render(fmt.Sprintf("remove '%s' from claudea? you stay signed in to it · y / n", it.name))
		}
		return sRed.Render(fmt.Sprintf("remove '%s'? y / n", it.name))
	}
	if m.mode == modeNaming {
		return sDim.Render("enter save · esc cancel")
	}
	keys := []string{"↑↓ move"}
	switch m.items[m.cursor].kind {
	case "saved":
		keys = append(keys, "enter switch", "1-9 jump", "d remove")
	case "unsaved":
		keys = append(keys, "enter save")
	case "add":
		keys = append(keys, "enter sign in")
	}
	keys = append(keys, "a add", "r refresh", "q quit")
	return sDim.Render(strings.Join(keys, " · "))
}

func (m model) View() string {
	header := sTitle.Render(" Claude accounts")
	if !slices.ContainsFunc(m.items, func(it item) bool { return it.kind != "add" }) {
		header += "\n\n" + sDim.Render(" No accounts saved yet. Press Enter to sign in to Claude in your browser —\n the account you sign in with is saved here, then add more the same way.")
	}
	wrapFooter := func(s string) string {
		if m.width > 2 {
			return lipgloss.NewStyle().Width(m.width - 1).Render(s)
		}
		return s
	}
	footer := " " + wrapFooter(m.help())
	if m.status != "" && m.mode != modeConfirmRemove {
		st := sGreen
		if m.statusIsErr {
			st = sRed
		}
		footer = " " + wrapFooter(st.Render(m.status)) + "\n" + footer
	}

	rendered := make([]string, len(m.items))
	number := 0
	for i, it := range m.items {
		if it.kind == "saved" {
			number++
		}
		rendered[i] = m.renderItem(i, number)
	}
	budget := m.height - lipgloss.Height(header) - lipgloss.Height(footer) - 2
	start := 0
	for m.height > 0 && start < m.cursor && linesOf(rendered[start:m.cursor+1]) > budget {
		start++
	}
	var shown []string
	used := 0
	for i := start; i < len(rendered); i++ {
		h := lipgloss.Height(rendered[i])
		if m.height > 0 && used+h > budget && i > m.cursor {
			shown = append(shown, sDim.Render("   ↓ more"))
			break
		}
		shown = append(shown, rendered[i])
		used += h
	}
	if start > 0 {
		shown = append([]string{sDim.Render("   ↑ more")}, shown...)
	}
	return header + "\n\n" + strings.Join(shown, "\n") + "\n\n" + footer
}

func linesOf(parts []string) int {
	n := 0
	for _, p := range parts {
		n += lipgloss.Height(p)
	}
	return n
}
