package menu

// TODO part of this, the drawing bit, will probably have to go in a
// different package, so we can use it to draw resize information

// FIXME get rid of all panics
// FIXME reorder code
// FIXME clean code up

import (
	"strings"
	"time"
	"unicode/utf16"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/xevent"
	"github.com/BurntSushi/xgbutil/xwindow"
)

const promptStart = "\xc2\xbb"
const promptEnd = "\xc2\xab"

type Config struct {
	X         int
	Y         int
	MinY      int
	MaxHeight int // TODO should this be MaxY?
	FilterFn  FilterFunc
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
	entries        []Entry // TODO not string but a struct, mapping display string to command
	displayEntries []Entry
	active         int
	gcI            xproto.Gcontext // FIXME choose better names
	gcN            xproto.Gcontext
	font           xproto.Font
	fontAscent     int16
	fontDescent    int16
	title          string
	input          string
	longestEntry   int
	filterFn       FilterFunc
	ch             chan Entry
}

// TODO document that input slice mustn't be modified
type FilterFunc func(entries []Entry, prompt string) []Entry
type ExecFunc func(Entry)

func New(xu *xgbutil.XUtil, title string, cfg Config) *Menu {
	var err error

	m := &Menu{
		xu:        xu,
		title:     title,
		y:         cfg.Y,
		x:         cfg.X,
		minY:      cfg.MinY,
		maxHeight: cfg.MaxHeight,
		filterFn:  cfg.FilterFn,
		ch:        make(chan Entry),
	}

	m.font, err = xproto.NewFontId(m.xu.Conn())
	if err != nil {
		panic(err)
	}

	// name := "-Misc-Fixed-Medium-R-Normal--20-200-75-75-C-100-ISO10646-1"
	name := "-Misc-Fixed-Bold-R-Normal--18-120-100-100-C-90-ISO10646-1"
	err = xproto.OpenFontChecked(m.xu.Conn(), m.font, uint16(len(name)), name).Check()
	if err != nil {
		panic(err)
	}

	ex, err := xproto.QueryTextExtents(m.xu.Conn(), xproto.Fontable(m.font), []xproto.Char2b{{0, 'z'}}, 0).Reply()
	if err != nil {
		panic(err)
	}
	m.fontAscent = ex.FontAscent
	m.fontDescent = ex.FontDescent

	return m
}

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

	s, _ := toChar2b(longest)
	ex, err := xproto.QueryTextExtents(m.xu.Conn(), xproto.Fontable(m.font), s, 0).Reply()
	if err != nil {
		panic(err)
	}

	m.height = (len(m.displayEntries) + 1) * int(m.fontAscent+m.fontDescent)
	if m.height > m.maxHeight {
		m.height = m.maxHeight
	}
	if m.y+m.height > m.maxHeight {
		m.y = m.maxHeight + m.minY - m.height
	}
	m.width = int(ex.OverallWidth)
	m.win.MoveResize(m.x, m.y, m.width, m.height)
}

func (m *Menu) Show() *xwindow.Window {
	var err error
	m.win, err = xwindow.Create(m.xu, m.xu.RootWin())
	if err != nil {
		panic(err)
	}

	m.gcN, err = xproto.NewGcontextId(m.xu.Conn())
	if err != nil {
		panic(err)
	}
	m.gcI, err = xproto.NewGcontextId(m.xu.Conn())
	if err != nil {
		panic(err)
	}
	mask := uint32(xproto.GcForeground | xproto.GcBackground | xproto.GcFont)
	err = xproto.CreateGCChecked(m.xu.Conn(), m.gcN, xproto.Drawable(m.win.Id), mask,
		[]uint32{0, 0xFFFFFF, uint32(m.font)}).Check()
	if err != nil {
		panic(err)
	}
	err = xproto.CreateGCChecked(m.xu.Conn(), m.gcI, xproto.Drawable(m.win.Id), mask,
		[]uint32{0xFFFFFF, 0, uint32(m.font)}).Check()
	if err != nil {
		panic(err)
	}

	m.win.Listen(xproto.EventMaskExposure, xproto.EventMaskKeyPress)

	// TODO support emacs keys
	keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		m.active++ // FIXME handle overflow?
		if m.active >= len(m.displayEntries) {
			m.active = 0
		}
		m.draw()
	}).Connect(m.xu, m.win.Id, "Down", false)

	keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		m.active--
		if m.active < 0 {
			m.active = len(m.displayEntries) - 1
		}
		m.draw()
	}).Connect(m.xu, m.win.Id, "Up", false)

	keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		if len(m.input) > 0 {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
			m.filter()
			m.draw()
		}
	}).Connect(m.xu, m.win.Id, "BackSpace", false)

	keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		close(m.ch)
	}).Connect(m.xu, m.win.Id, "Escape", false)

	fn := keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		if m.active > len(m.displayEntries)-1 {
			m.ch <- Entry{m.input, m.input, true}
			return
		}
		m.ch <- m.displayEntries[m.active]
	})
	fn.Connect(m.xu, m.win.Id, "Return", false)
	fn.Connect(m.xu, m.win.Id, "KP_Enter", false)

	xevent.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		key := keybind.LookupString(xu, ev.State, ev.Detail)
		if len([]rune(key)) == 1 {
			m.input += key
			m.filter()
			m.draw()
		}
	}).Connect(m.xu, m.win.Id)

	xevent.ExposeFun(func(xu *xgbutil.XUtil, ev xevent.ExposeEvent) {
		m.draw()
	}).Connect(m.xu, m.win.Id)

	// TODO see about setting hints so we get mapped in the right place
	m.win.Map()
	m.win.MoveResize(m.x, m.y, m.width, m.height)

	for i := 0; i < 500; i++ {
		reply, err := xproto.GrabKeyboard(m.xu.Conn(), true, m.win.Id, xproto.TimeCurrentTime,
			xproto.GrabModeSync, xproto.GrabModeAsync).Reply()
		if err != nil {
			panic(err) // FIXME don't panic
		}
		if reply.Status == xproto.GrabStatusSuccess {
			break
		}

		time.Sleep(time.Millisecond)
	}

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

func toChar2b(runes []rune) ([]xproto.Char2b, int) {
	ucs2 := utf16.Encode(runes)
	var chars []xproto.Char2b
	for _, r := range ucs2 {
		chars = append(chars, xproto.Char2b{byte(r >> 8), byte(r)})
	}
	return chars, len(runes)
}

func pad(r []rune, l int) []rune {
	// FIXME for some entries, this doesn't seem to work correctly,
	// investigate...
	if len(r) < l {
		for i := len(r); i <= l; i++ {
			r = append(r, ' ')
		}
	}

	return r
}

func (m *Menu) prompt() string {
	return m.title + promptStart + m.input + promptEnd
}

func (m *Menu) draw() {
	xproto.PolyFillRectangle(m.xu.Conn(), xproto.Drawable(m.win.Id), m.gcI,
		[]xproto.Rectangle{{0, 0, uint16(m.width), uint16(m.height)}})

	r := pad([]rune(m.prompt()), m.longestEntry)
	chars, n := toChar2b(r)
	err := xproto.ImageText16Checked(m.xu.Conn(), byte(n), xproto.Drawable(m.win.Id), m.gcN, 0,
		m.fontAscent, chars).Check()
	if err != nil {
		panic(err)
	}

	if len(m.displayEntries) == 0 {
		return
	}
	idx := m.active
	num := m.height/int(m.fontAscent+m.fontDescent) - 1
	if num >= len(m.displayEntries) {
		// Technically, the window shouldn't be big enough to allow
		// repeating elements, but be safe regardless.
		num = len(m.displayEntries) - 1
	}
	for i := 0; i <= num; i++ {
		entry := m.displayEntries[idx]
		r := []rune(entry.Display)
		r = pad(r, m.longestEntry)
		chars, n := toChar2b(r)
		y := int16(i+1)*(m.fontAscent+m.fontDescent) + m.fontAscent
		gc := m.gcN
		if i == 0 {
			gc = m.gcI
		}
		err = xproto.ImageText16Checked(m.xu.Conn(), byte(n), xproto.Drawable(m.win.Id), gc, 0,
			y, chars).Check()
		if err != nil {
			panic(err)
		}

		idx = (idx + 1) % len(m.displayEntries)
	}
}
