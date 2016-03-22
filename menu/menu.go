package menu

import (
	"strings"
	"time"

	"honnef.co/go/gwm/draw"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/xevent"
	"github.com/BurntSushi/xgbutil/xwindow"
)

const promptStart = "\xc2\xbb"
const promptEnd = "\xc2\xab"

type Config struct {
	X           int
	Y           int
	MinY        int
	MaxHeight   int
	BorderWidth int
	BorderColor int
	Font        xproto.Font
	FilterFn    FilterFunc
}

type Entry struct {
	Display   string
	Payload   interface{}
	synthetic bool
}

func (e Entry) Synthetic() bool {
	return e.synthetic
}

type Menu struct {
	xu             *xgbutil.XUtil
	x              int
	y              int
	width          int
	height         int
	minY           int
	maxHeight      int
	win            *xwindow.Window
	entries        []Entry
	displayEntries []Entry
	active         int
	font           xproto.Font
	title          string
	input          string
	longestEntry   int
	borderWidth    int
	borderColor    int
	filterFn       FilterFunc
	ch             chan Entry
	gcs            draw.GCs
}

// TODO document that input slice mustn't be modified
type FilterFunc func(entries []Entry, prompt string) []Entry
type ExecFunc func(Entry)

func FilterPrefix(entries []Entry, prompt string) []Entry {
	if prompt == "" {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	prompt = strings.ToLower(prompt)
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Display), prompt) {
			out = append(out, entry)
		}
	}
	return out
}

func New(xu *xgbutil.XUtil, title string, cfg Config) (*Menu, error) {
	m := &Menu{
		xu:          xu,
		title:       title,
		y:           cfg.Y,
		x:           cfg.X,
		minY:        cfg.MinY,
		maxHeight:   cfg.MaxHeight,
		borderWidth: cfg.BorderWidth,
		borderColor: cfg.BorderColor,
		font:        cfg.Font,
		filterFn:    cfg.FilterFn,
		ch:          make(chan Entry),
		gcs:         make(draw.GCs),
	}

	var err error
	m.win, err = xwindow.Generate(m.xu)
	if err != nil {
		return nil, err
	}

	err = m.win.CreateChecked(m.xu.RootWin(), m.x, m.y, 1, 1, 0)
	if err != nil {
		return nil, err
	}
	xproto.ConfigureWindow(m.xu.Conn(), m.win.Id, xproto.ConfigWindowBorderWidth, []uint32{uint32(m.borderWidth)})
	m.win.Change(xproto.CwBorderPixel, uint32(m.borderColor))

	err = m.connectEvents()
	if err != nil {
		m.win.Destroy()
		return nil, err
	}
	return m, nil
}

func (m *Menu) up(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	m.active--
	if m.active < 0 {
		m.active = len(m.displayEntries) - 1
	}
	m.draw()
}

func (m *Menu) down(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	m.active++ // FIXME handle overflow?
	if m.active >= len(m.displayEntries) {
		m.active = 0
	}
	m.draw()
}

func (m *Menu) backspace(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	if len(m.input) > 0 {
		r := []rune(m.input)
		m.input = string(r[:len(r)-1])
		m.filter()
		m.draw()
	}
}

func (m *Menu) enter(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	if m.active > len(m.displayEntries)-1 {
		m.ch <- Entry{m.input, m.input, true}
		return
	}
	m.ch <- m.displayEntries[m.active]
}

func (m *Menu) escape(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	close(m.ch)
}

func (m *Menu) keypress(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
	key := keybind.LookupString(xu, ev.State, ev.Detail)
	if (ev.State & xproto.ModMaskControl) > 0 {
		return
	}
	if len([]rune(key)) > 1 {
		return
	}
	m.input += key
	m.filter()
	m.draw()
}

func (m *Menu) connectEvents() error {
	err := m.win.Listen(xproto.EventMaskExposure, xproto.EventMaskKeyPress)
	if err != nil {
		return err
	}

	// TODO support emacs keys
	for _, grab := range []struct {
		fn  keybind.KeyPressFun
		key string
	}{
		{m.up, "Up"},
		{m.up, "control-r"},
		{m.down, "Down"},
		{m.down, "control-s"},
		{m.backspace, "BackSpace"},
		{m.escape, "Escape"},
		{m.enter, "Return"},
		{m.enter, "KP_Enter"},
	} {
		err := grab.fn.Connect(m.xu, m.win.Id, grab.key, false)
		if err != nil {
			// Without a grab, the only possible error is an error
			// parsing the key name, which is a programmer error.
			panic(err)
		}
	}

	xevent.KeyPressFun(m.keypress).Connect(m.xu, m.win.Id)
	xevent.ExposeFun(func(xu *xgbutil.XUtil, ev xevent.ExposeEvent) {
		m.draw()
	}).Connect(m.xu, m.win.Id)

	return nil
}

func (m *Menu) GCs() draw.GCs {
	return m.gcs
}

func (m *Menu) Win() xproto.Window {
	return m.win.Id
}

func (m *Menu) X() *xgbutil.XUtil {
	return m.xu
}

func (m *Menu) SetEntries(entries []Entry) {
	// TODO document that elements must be sorted
	m.entries = entries
	m.filter()
}

func (m *Menu) filter() {
	m.displayEntries = m.filterFn(m.entries, m.input)
	m.active = 0

	longest := []rune(m.prompt())
	m.longestEntry = len(longest)
	for _, entry := range m.displayEntries {
		r := []rune(entry.Display)
		if len(r) > m.longestEntry {
			m.longestEntry = len(r)
			longest = []rune(entry.Display)
		}
	}
}

func (m *Menu) Show() *xwindow.Window {
	m.win.Map()

	for i := 0; i < 500; i++ {
		reply, err := xproto.GrabKeyboard(m.xu.Conn(), true, m.win.Id, xproto.TimeCurrentTime,
			xproto.GrabModeSync, xproto.GrabModeAsync).Reply()
		if err != nil {
			// err ∈ {BadValue, BadWindow} → can only be a programmer error
			panic(err)
		}
		if reply.Status == xproto.GrabStatusSuccess {
			break
		}

		time.Sleep(time.Millisecond)
	}

	m.draw()
	return m.win
}

func (m *Menu) Wait() (Entry, bool) {
	ret, ok := <-m.ch
	m.win.Unmap()
	// We cannot use xgbutil's Destroy() method because that will
	// detach all events, including DestroyNotify that our WM needs
	xproto.DestroyWindow(m.xu.Conn(), m.win.Id)
	return ret, ok
}

func pad(s string, l int) string {
	// FIXME for some entries, this doesn't seem to work correctly,
	// investigate...
	r := []rune(s)
	if len(r) < l {
		for i := len(r); i <= l; i++ {
			r = append(r, ' ')
		}
	}

	return string(r)
}

func (m *Menu) prompt() string {
	return m.title + promptStart + m.input + promptEnd
}

func (m *Menu) resize() {
	if m.height > m.maxHeight {
		m.height = m.maxHeight
	}

	if m.y+m.height > m.maxHeight {
		m.y = m.maxHeight + m.minY - m.height
	}

	m.win.MoveResize(m.x, m.y, m.width, m.height)
}

func (m *Menu) draw() {
	defer m.resize()

	draw.Fill(m, m.width, m.height, 0xFFFFFF)

	m.width, m.height = draw.Text(m, m.prompt(), m.font, 0, 0xFFFFFF, 0, 0)

	if len(m.displayEntries) == 0 {
		return
	}
	idx := m.active

	start := idx
	for i := 0; m.height < m.maxHeight; i++ {
		entry := m.displayEntries[idx]
		s := pad(entry.Display, m.longestEntry)
		fg := 0
		bg := 0xFFFFFF
		if i == 0 {
			fg = 0xFFFFFF
			bg = 0
		}
		if len(s) > 255 {
			s = s[:255]
		}

		w, h := draw.Text(m, s, m.font, fg, bg, 0, m.height)
		m.height += h
		if w > m.width {
			m.width = w
		}

		idx = (idx + 1) % len(m.displayEntries)
		if idx == start {
			break
		}
	}
}
