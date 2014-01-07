package main

/*
Notes

This is a list of things that I don't want to forget because they
tripped me up:

- xgb/xgbutil seems to dispatch ClientMessage events to the window it
  was targetted at, not the root window it was actually sent to

*/
import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/ewmh"
	"github.com/BurntSushi/xgbutil/icccm"
	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/mousebind"
	"github.com/BurntSushi/xgbutil/xcursor"
	"github.com/BurntSushi/xgbutil/xevent"
	"github.com/BurntSushi/xgbutil/xinerama"
	"github.com/BurntSushi/xgbutil/xprop"
	"github.com/BurntSushi/xgbutil/xwindow"

	"honnef.co/go/gwm/config"
	"honnef.co/go/gwm/menu"
)

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func abs(x int) int {
	if x >= 0 {
		return x
	}

	return -x
}

// TODO replace all uses of must() and should() with meaningful error
// handling/logging.
func must(err error) {
	if err == nil {
		return
	}

	panic(err)
}

func should(err error) {
	if err == nil {
		return
	}

	log.Println("Error:", err)
}

func subtractGaps(sc Geometry, gap config.Gap) Geometry {
	sc.X += gap.Left
	sc.Y += gap.Top
	sc.Width -= gap.Left + gap.Right
	sc.Height -= gap.Top + gap.Bottom
	return sc
}

func snapcalc(n0, n1, e0, e1, snapdist int) int {
	var s0, s1 int

	if abs(e0-n0) <= snapdist {
		s0 = e0 - n0
	}

	if abs(e1-n1) <= snapdist {
		s1 = e1 - n1
	}

	if s0 != 0 && s1 != 0 {
		if abs(s0) < abs(s1) {
			return s0
		} else {
			return s1
		}
	} else if s0 != 0 {
		return s0
	} else if s1 != 0 {
		return s1
	}

	return 0
}

func LogWindowEvent(win *Window, s interface{}) {
	log.Printf("%d (%s): %s", win.Id, win.Name(), s)
}

func printSizeHints(hints *icccm.NormalHints) {
	log.Printf("Size hints with flags %d", hints.Flags)
	if (hints.Flags & (icccm.SizeHintUSPosition | icccm.SizeHintPPosition)) > 1 {
		log.Printf("\tx = %d y = %d", hints.X, hints.Y)
	}
	if (hints.Flags & (icccm.SizeHintUSSize | icccm.SizeHintPSize)) > 1 {
		log.Printf("\tw = %d h = %d", hints.Width, hints.Height)
	}
	if (hints.Flags & icccm.SizeHintPMinSize) > 0 {
		log.Printf("\tmw = %d mh = %d", hints.MinWidth, hints.MinHeight)
	}
	if (hints.Flags & icccm.SizeHintPMaxSize) > 0 {
		log.Printf("\tMw = %d Mh = %d", hints.MaxWidth, hints.MaxHeight)
	}
	if (hints.Flags & icccm.SizeHintPResizeInc) > 0 {
		log.Printf("\tiw = %d ih = %d", hints.WidthInc, hints.HeightInc)
	}
	if (hints.Flags & icccm.SizeHintPAspect) > 0 {
		log.Printf("\taspect information")
	}
	if (hints.Flags & icccm.SizeHintPBaseSize) > 0 {
		log.Printf("\tbw = %d bh = %d", hints.BaseWidth, hints.BaseHeight)
	}
}

func executables() []menu.Entry {
	var executables []string
	for _, path := range strings.Split(os.Getenv("PATH"), ":") {
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		filepath.Walk(path, func(cur string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if cur == path {
				return nil
			}
			if info.IsDir() {
				return filepath.SkipDir
			}
			if (info.Mode() & 0111) > 0 {
				executables = append(executables, filepath.Base(cur))
			}
			return nil
		})
	}

	sort.StringSlice(executables).Sort()

	entries := make([]menu.Entry, len(executables))
	for i, e := range executables {
		entries[i] = menu.Entry{Display: e, Payload: e}
	}
	return entries
}

func screenForPoint(screens []Geometry, x, y int) Geometry {
	var screen Geometry
	for _, screen = range screens {
		if (x >= screen.X && x <= screen.X+screen.Width) &&
			(y >= screen.Y && y <= screen.Y+screen.Height) {
			break
		}
	}

	return screen
}

type corner int

const (
	cornerNone = 0
	cornerN    = 1
	cornerW    = 2
	cornerS    = 4
	cornerE    = 8

	cornerNW = cornerN | cornerW
	cornerNE = cornerN | cornerE
	cornerSW = cornerS | cornerW
	cornerSE = cornerS | cornerE
)

type drag struct {
	startX, startY   int
	offsetX, offsetY int
	corner           corner
}

type Layer int

const (
	LayerDesktop Layer = -2
	LayerBelow   Layer = -1
	LayerNormal  Layer = 0
	LayerAbove   Layer = 1
)

type State int

type MaximizedState int

const (
	MaximizedH    MaximizedState = 1
	MaximizedV    MaximizedState = 2
	MaximizedFull MaximizedState = 3
)

type Geometry struct {
	X, Y          int
	Width, Height int
}

type Window struct {
	*xwindow.Window
	State             State
	Layer             Layer
	Mapped            bool
	Geom              Geometry
	BorderWidth       int
	wm                *WM
	curDrag           *drag
	unmaximizedGeom   Geometry
	unfullscreenGeom  Geometry
	maximized         MaximizedState
	fullscreen        bool
	unfullscreenLayer Layer
	frozen            bool
}

func (win *Window) Name() string {
	// TODO instead of needing to call this function repeatedly, be
	// notified and cache when the name changes
	name, err := ewmh.WmNameGet(win.wm.X, win.Id)
	if name == "" || err != nil {
		name, _ = icccm.WmNameGet(win.wm.X, win.Id)
	}

	return name
}

func (win *Window) SetBorderColor(color int) {
	win.Change(xproto.CwBorderPixel, uint32(color))
}

func (win *Window) SetBorderWidth(width int) {
	win.BorderWidth = width
	xproto.ConfigureWindow(win.wm.X.Conn(), win.Id, xproto.ConfigWindowBorderWidth, []uint32{uint32(width)})
}

func (win *Window) Raise() {
	windows := make(map[Layer][]*Window)
	for _, ow := range win.wm.GetWindows(icccm.StateNormal) {
		if ow.Id == win.Id || ow.Id == win.wm.Root.Id {
			continue
		}

		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	windows[win.Layer] = append(windows[win.Layer], win)

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		for _, ow := range windows[layer] {
			update = append(update, ow)
		}
	}
	win.wm.Restack(update)
}

func (win *Window) Lower() {
	windows := make(map[Layer][]*Window)
	windows[win.Layer] = []*Window{win}
	for _, ow := range win.wm.GetWindows(icccm.StateNormal) {
		if ow.Id == win.Id || ow.Id == win.wm.Root.Id {
			continue
		}

		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		for _, ow := range windows[layer] {
			update = append(update, ow)
		}
	}
	win.wm.Restack(update)
}

func (win *Window) MoveBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
	win.Raise()
	win.curDrag = &drag{win.Geom.X, win.Geom.Y, rootX, rootY, cornerNone}
	return true, win.wm.Cursors["fleur"]
}

func (win *Window) MoveStep(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	if win.frozen {
		return
	}
	dx := rootX - win.curDrag.offsetX
	dy := rootY - win.curDrag.offsetY

	// FIXME do we need to consider the border here?
	win.Geom.X = win.curDrag.startX + dx
	win.Geom.Y = win.curDrag.startY + dy

	screen := win.Screen()
	screen = subtractGaps(screen, win.wm.Config.Gap)

	win.Geom.X += snapcalc(win.Geom.X, win.Geom.X+win.Geom.Width+win.BorderWidth*2,
		screen.X, screen.X+screen.Width, win.wm.Config.Snapdist)
	win.Geom.Y += snapcalc(win.Geom.Y, win.Geom.Y+win.Geom.Height+win.BorderWidth*2,
		screen.Y, screen.Y+screen.Height, win.wm.Config.Snapdist)
	win.move()
}

func (win *Window) MoveEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	win.curDrag = nil
}

func (win *Window) ResizeBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
	if eventX < 0 {
		eventX = 0
	}
	if eventY < 0 {
		eventY = 0
	}

	var (
		corner           corner
		x, y             int
		cursorX, cursorY string
	)

	if eventX > win.Geom.Width/2 {
		corner |= cornerE
		cursorX = "right"
		x = win.Geom.Width
	} else {
		corner |= cornerW
		cursorX = "left"
	}

	if eventY > win.Geom.Height/2 {
		corner |= cornerS
		cursorY = "bottom"
		y = win.Geom.Height
	} else {
		corner |= cornerN
		cursorY = "top"
	}

	win.curDrag = &drag{win.Geom.X, win.Geom.Y, rootX, rootY, corner}
	xproto.WarpPointer(win.wm.X.Conn(), xproto.WindowNone, win.Id, 0, 0, 0, 0, int16(x), int16(y))
	return true, win.wm.Cursors[cursorY+"_"+cursorX+"_corner"]
}

func roundDown(num int, multiple int) int {
	if multiple == 0 {
		return num
	}
	return num - (num % multiple)
}

func (win *Window) ResizeStep(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	// Notes:
	// - the resize step calculations assume that the window already
	//   has a valid (base + multiple of step) size.
	// - it also assumes that the min and max sizes are valid multiples.

	if win.frozen {
		return
	}

	rootX -= win.wm.Config.BorderWidth
	rootY -= win.wm.Config.BorderWidth
	// FIXME do not query normal hints on each step, instead cache it
	// and listen to changes
	var (
		dw, dh, dx, dy                                   int
		hInc, wInc, hMin, wMin, hMax, wMax, hBase, wBase int
		hasMax, hasAspect                                bool
		minAspect, maxAspect                             float64
	)
	hMin = 1
	wMin = 1

	normalHints, err := icccm.WmNormalHintsGet(xu, win.Id)
	if err == nil {
		if (normalHints.Flags & icccm.SizeHintPResizeInc) > 0 {
			hInc = int(normalHints.HeightInc)
			wInc = int(normalHints.WidthInc)
		}

		if (normalHints.Flags & icccm.SizeHintPBaseSize) > 0 {
			hBase = int(normalHints.BaseHeight)
			wBase = int(normalHints.BaseWidth)

			hMin = int(normalHints.BaseHeight)
			wMin = int(normalHints.BaseWidth)
		}

		if (normalHints.Flags & icccm.SizeHintPMinSize) > 0 {
			hMin = int(normalHints.MinHeight)
			wMin = int(normalHints.MinWidth)
		}

		if (normalHints.Flags & icccm.SizeHintPMaxSize) > 0 {
			hasMax = true
			hMax = int(normalHints.MaxHeight)
			wMax = int(normalHints.MaxWidth)
		}

		if (normalHints.Flags & icccm.SizeHintPAspect) > 0 {
			hasAspect = true
			minAspect = float64(normalHints.MinAspectNum) / float64(normalHints.MinAspectDen)
			maxAspect = float64(normalHints.MaxAspectNum) / float64(normalHints.MaxAspectDen)
		}
	}

	// FIXME consider size hints

	// TODO the meat of this calculation should be moved to a
	// different function, so that keyboard resizing can reuse it

	if (win.curDrag.corner & cornerW) > 0 {
		dw = win.Geom.X - rootX
		dw = roundDown(dw, wInc)
		dx = -dw
	}

	if (win.curDrag.corner & cornerE) > 0 {
		dw = rootX - (win.Geom.X + win.Geom.Width)
		dw = roundDown(dw, wInc)
	}

	if (win.curDrag.corner & cornerS) > 0 {
		dh = rootY - (win.Geom.Y + win.Geom.Height)
		dh = roundDown(dh, hInc)
	}

	if (win.curDrag.corner & cornerN) > 0 {
		dh = win.Geom.Y - rootY
		dh = roundDown(dh, hInc)
		dy = -dh
	}

	nh := win.Geom.Height + dh
	nw := win.Geom.Width + dw

	if hasAspect {
		nw -= wBase
		nh -= hBase
		aspect := float64(nw) / float64(nh)
		if maxAspect < aspect {
			nw = int(float64(nh) * maxAspect)
		} else if minAspect > aspect {
			nw = int(float64(nh) * minAspect)
		}

		if dx != 0 {
			dx -= nw - (win.Geom.Width + dw)
		}

		nw += wBase
		nh += hBase
	}

	if nh >= hMin && (!hasMax || nh <= hMax) {
		win.Geom.Height = nh
		win.Geom.Y += dy
	}

	if nw >= wMin && (!hasMax || nw <= wMax) {
		win.Geom.Width = nw
		win.Geom.X += dx
	}

	win.moveAndResize()
}

func (win *Window) ResizeEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	win.curDrag = nil
}

func (win *Window) Move(x, y int) {
	// TODO document that this function will reset the maximized state
	win.Geom.X = x
	win.Geom.Y = y
	win.move()
}

func (win *Window) MoveAndResize(x, y, width, height int) {
	// FIXME respect min/max size and increments
	// TODO document that this function will reset the maximized state
	win.Geom.X = x
	win.Geom.Y = y
	win.Geom.Width = width
	win.Geom.Height = height
	win.moveAndResize()
}

func (win *Window) Freeze() {
	win.frozen = true
}

func (win *Window) Unfreeze() {
	win.frozen = false
}

func (win *Window) ToggleFreeze() {
	win.frozen = !win.frozen
}

func (win *Window) Fullscreen() {
	if win.fullscreen {
		return
	}

	// TODO what about min/max size and increments?

	sc := win.Screen()
	win.unfullscreenGeom = win.Geom
	win.SetBorderWidth(0)
	win.Geom.X = sc.X
	win.Geom.Y = sc.Y
	win.Geom.Width = sc.Width
	win.Geom.Height = sc.Height
	win.moveAndResizeNoReset()
	win.fullscreen = true
	win.Freeze()
	win.unfullscreenLayer = win.Layer
	win.SetLayer(LayerAbove)
	win.Raise()
	win.updateWmState()
}

func (win *Window) Unfullscreen() {
	if !win.fullscreen {
		return
	}

	win.Geom = win.unfullscreenGeom
	win.SetBorderWidth(win.wm.Config.BorderWidth)
	win.moveAndResizeNoReset()
	win.fullscreen = false
	win.Unfreeze()
	win.SetLayer(win.unfullscreenLayer)
	win.updateWmState()
}

func (win *Window) ToggleFullscreen() {
	if win.fullscreen {
		win.Unfullscreen()
	} else {
		win.Fullscreen()
	}
}

func (win *Window) Maximize(state MaximizedState) {
	// TODO what about min/max size and increments?

	// Only store the geometry if we're not maximized at all yet
	if win.maximized == 0 {
		win.unmaximizedGeom = win.Geom
	}

	sc := subtractGaps(win.Screen(), win.wm.Config.Gap)
	if (state & MaximizedH) > 0 {
		win.Geom.X = sc.X
		win.Geom.Width = sc.Width - 2*win.wm.Config.BorderWidth
	}
	if (state & MaximizedV) > 0 {
		win.Geom.Y = sc.Y
		win.Geom.Height = sc.Height - 2*win.wm.Config.BorderWidth
	}
	win.moveAndResizeNoReset()
	win.maximized |= state
	win.updateWmState()
}

func (win *Window) Unmaximize(state MaximizedState) {
	if (state & MaximizedH) > 0 {
		win.Geom.X = win.unmaximizedGeom.X
		win.Geom.Width = win.unmaximizedGeom.Width
	}
	if (state & MaximizedV) > 0 {
		win.Geom.Y = win.unmaximizedGeom.Y
		win.Geom.Height = win.unmaximizedGeom.Height
	}
	win.moveAndResize()
	if !win.ContainsPointer() {
		win.CenterPointer()
	}
	win.maximized &= ^state
	win.updateWmState()
}

func (win *Window) ToggleMaximize(state MaximizedState) {
	if state > win.maximized || win.maximized&state == 0 {
		win.Maximize(state)
	} else {
		win.Unmaximize(state)
	}
}

func (win *Window) ContainsPointer() bool {
	px, py := win.wm.PointerPos()
	return !(px < win.Geom.X || px > win.Geom.X+win.Geom.Width ||
		py < win.Geom.Y || py > win.Geom.Y+win.Geom.Height)
}

func (win *Window) CenterPointer() {
	xproto.WarpPointer(win.wm.X.Conn(), xproto.WindowNone, win.Id, 0, 0, 0, 0,
		int16(win.Geom.Width/2-win.wm.Config.BorderWidth), int16(win.Geom.Height/2-win.wm.Config.BorderWidth))
}

// move moves the window based on its current Geom. It also resets the
// window's maximized state.
func (win *Window) move() {
	win.Window.Move(win.Geom.X, win.Geom.Y)
	win.maximized &= ^MaximizedFull
	win.updateWmState()
}

// moveAndResize moves and resizes the window based on its current
// Geom. It also resets the window's maximized state.
func (win *Window) moveAndResize() {
	win.Window.MoveResize(win.Geom.X, win.Geom.Y, win.Geom.Width, win.Geom.Height)
	win.maximized &= ^MaximizedFull
	win.updateWmState()
}

// move moves the window based on its current Geom.
func (win *Window) moveNoReset() {
	win.Window.Move(win.Geom.X, win.Geom.Y)
}

// moveAndResize moves and resizes the window based on its current
// Geom.
func (win *Window) moveAndResizeNoReset() {
	win.Window.MoveResize(win.Geom.X, win.Geom.Y, win.Geom.Width, win.Geom.Height)
}

func (win *Window) EnterNotify(xu *xgbutil.XUtil, ev xevent.EnterNotifyEvent) {
	LogWindowEvent(win, "Enter")
	win.markActive()
}

func (win *Window) Activate() {
	// FIXME what do we do if the window is hidden behind a different
	// layer?
	win.Raise()
	win.CenterPointer()
}

func (win *Window) markActive() {
	if win == win.wm.CurWindow {
		return
	}
	// TODO how do we close clients that don't accept focus?
	if !win.Focusable() {
		LogWindowEvent(win, "not focusable, skipping")
		return
	}
	win.SetBorderColor(win.wm.Config.Colors["activeborder"])
	win.Focus()
	if curwin := win.wm.CurWindow; curwin != nil {
		curwin.SetBorderColor(win.wm.Config.Colors["inactiveborder"])
	}
	win.wm.CurWindow = win
}

func (win *Window) Focus() {
	if win.SupportsProtocol("WM_TAKE_FOCUS") {
		win.SendMessage("WM_TAKE_FOCUS")
	} else {
		win.Window.Focus()
	}
	should(ewmh.ActiveWindowSet(win.wm.X, win.Id))
}

func (win *Window) Focusable() bool {
	hints, err := icccm.WmHintsGet(win.wm.X, win.Id)
	if err != nil {
		LogWindowEvent(win, "Could not read hints")
		return true
	}
	return (hints.Flags&icccm.HintInput) == 0 || hints.Input == 1 || win.SupportsProtocol("WM_TAKE_FOCUS")
}

func (win *Window) DestroyNotify(xu *xgbutil.XUtil, ev xevent.DestroyNotifyEvent) {
	LogWindowEvent(win, "Destroying")
	win.Detach()
	delete(win.wm.Windows, win.Id)
}

func (win *Window) UnmapNotify(xu *xgbutil.XUtil, ev xevent.UnmapNotifyEvent) {
	LogWindowEvent(win, "Unmapping")
	win.Mapped = false
	win.State = icccm.StateIconic
	should(icccm.WmStateSet(win.wm.X, win.Id, &icccm.WmState{State: uint(win.State)}))
}

func (win *Window) Init() {
	// TODO do something if the state is iconified
	LogWindowEvent(win, "Initializing")
	should(win.Listen(xproto.EventMaskEnterWindow,
		xproto.EventMaskStructureNotify))
	win.SetBorderWidth(win.wm.Config.BorderWidth)
	win.SetBorderColor(win.wm.Config.Colors["inactiveborder"])

	attr, err := xproto.GetGeometry(win.wm.X.Conn(), xproto.Drawable(win.Id)).Reply()
	if err != nil {
		should(err)
	} else {
		win.Geom.X = int(attr.X)
		win.Geom.Y = int(attr.Y)
		win.Geom.Width = int(attr.Width)
		win.Geom.Height = int(attr.Height)
	}

	states, err := ewmh.WmStateGet(win.X, win.Id)
	if err != nil {
		LogWindowEvent(win, "Could not get _NET_WM_STATE")
	} else {
		for _, state := range states {
			win.addState(state)
		}
	}

	if ms, ok := win.wm.Config.MouseBinds["window_move"]; ok {
		mousebind.Drag(win.wm.X, win.Id, win.Id, ms.ToXGB(), true,
			win.MoveBegin, win.MoveStep, win.MoveEnd)
	}

	if ms, ok := win.wm.Config.MouseBinds["window_resize"]; ok {
		mousebind.Drag(win.wm.X, win.Id, win.Id, ms.ToXGB(), true,
			win.ResizeBegin, win.ResizeStep, win.ResizeEnd)
	}

	if ms, ok := win.wm.Config.MouseBinds["window_lower"]; ok {
		fn := func(xu *xgbutil.XUtil, ev xevent.ButtonPressEvent) { win.Lower() }
		should(mousebind.ButtonPressFun(fn).Connect(win.wm.X, win.Id, ms.ToXGB(), false, true))
	}

	should(icccm.WmStateSet(win.wm.X, win.Id, &icccm.WmState{State: uint(win.State)}))
}

func (win *Window) ClientMessage(xu *xgbutil.XUtil, ev xevent.ClientMessageEvent) {
	name, err := xprop.AtomName(xu, ev.Type)
	if err != nil {
		log.Printf("Could not map atom %v to name", ev.Type)
		return
	}
	data := ev.Data.Data32
	switch name {
	case "_NET_WM_STATE":
		prop1, err1 := xprop.AtomName(win.wm.X, xproto.Atom(data[1]))
		prop2, err2 := xprop.AtomName(win.wm.X, xproto.Atom(data[2]))
		if err1 != nil {
			prop1 = ""
		}
		if err2 != nil {
			prop2 = ""
		}

		win.handleState(prop1, data)
		win.handleState(prop2, data)
	case "_NET_CLOSE_WINDOW":
		win.Delete()
	case "_NET_WM_MOVERESIZE":
		// Notes:
		// - currently we only support mouse-initiated actions
		// - for resize, we ignore data[2] (direction), because we
		//   determine the corner based on X/Y of the event, and we
		//   don't support resizing on a single axis
		// - for some branches of the switch, we might be populating
		//   `ev` with bogus data, but since we don't use ev in these
		//   branches, it doesn't matter

		ev := &xproto.ButtonPressEvent{
			Sequence: 0,
			Detail:   xproto.Button(data[3]),
			Root:     win.wm.Root.Id,
			RootX:    int16(data[0]),
			RootY:    int16(data[1]),
			EventX:   int16(data[0]) - int16(win.Geom.X),
			EventY:   int16(data[1]) - int16(win.Geom.Y),
			// FIXME what about Event, Child, State and SameScreen?
		}

		switch data[2] {
		case ewmh.Move:
			mousebind.DragBegin(win.X, xevent.ButtonPressEvent{ev}, win.Id, win.Id,
				win.MoveBegin, win.MoveStep, win.MoveEnd)
			return
		case ewmh.MoveKeyboard, ewmh.SizeKeyboard:
			return
		case ewmh.Cancel:
			mousebind.DragEnd(win.X, xevent.ButtonReleaseEvent{(*xproto.ButtonReleaseEvent)(ev)})
		default:
			mousebind.DragBegin(win.X, xevent.ButtonPressEvent{ev}, win.Id, win.Id,
				win.ResizeBegin, win.ResizeStep, win.ResizeEnd)
		}
	default:
		LogWindowEvent(win, "Unknown ClientMessage: "+name)
	}
}

func (win *Window) Delete() {
	if !win.wmDeleteWindow() {
		win.Kill()
	}
}

func (win *Window) SendMessage(name string) bool {
	protAtm, err := xprop.Atm(win.wm.X, "WM_PROTOCOLS")
	if err != nil {
		LogWindowEvent(win, err)
		return false
	}

	nAtm, err := xprop.Atm(win.wm.X, name)
	if err != nil {
		LogWindowEvent(win, err)
		return false
	}

	cm, err := xevent.NewClientMessage(32, win.Id, protAtm, int(nAtm))
	if err != nil {
		LogWindowEvent(win, err)
		return false
	}

	err = xproto.SendEventChecked(win.wm.X.Conn(), false, win.Id, 0, string(cm.Bytes())).Check()
	if err != nil {
		LogWindowEvent(win, err)
		return false
	}

	return true
}

func (win *Window) wmDeleteWindow() bool {
	if !win.SupportsProtocol("WM_DELETE_WINDOW") {
		return false
	}

	return win.SendMessage("WM_DELETE_WINDOW")
}

func (win *Window) Protocols() []string {
	protocols, err := icccm.WmProtocolsGet(win.wm.X, win.Id)
	if err != nil {
		LogWindowEvent(win, "Could not query WM_PROTOCOLS")
		return nil
	}
	return protocols
}

func (win *Window) SupportsProtocol(name string) bool {
	for _, p := range win.Protocols() {
		if p == name {
			return true
		}
	}
	return false
}

func (win *Window) handleState(prop string, data []uint32) {
	switch data[0] {
	case 0:
		win.removeState(prop)
	case 1:
		win.addState(prop)
	case 2:
		win.toggleState(prop)
	}
}

func (win *Window) removeState(prop string) {
	switch prop {
	case "_NET_WM_STATE_FULLSCREEN":
		win.Unfullscreen()
	case "_NET_WM_STATE_MAXIMIZED_HORZ":
		win.Unmaximize(MaximizedH)
	case "_NET_WM_STATE_MAXIMIZED_VERT":
		win.Unmaximize(MaximizedV)
	case "_NET_WM_STATE_ABOVE", "_NET_WM_STATE_BELOW":
		win.SetLayer(LayerNormal)
	default:
		LogWindowEvent(win, "Unknown _NET_WM_STATE: "+prop)
	}
}

func (win *Window) addState(prop string) {
	switch prop {
	case "_NET_WM_STATE_FULLSCREEN":
		win.Fullscreen()
	case "_NET_WM_STATE_MAXIMIZED_HORZ":
		win.Maximize(MaximizedH)
	case "_NET_WM_STATE_MAXIMIZED_VERT":
		win.Maximize(MaximizedV)
	case "_NET_WM_STATE_ABOVE":
		win.SetLayer(LayerAbove)
	case "_NET_WM_STATE_BELOW":
		win.SetLayer(LayerBelow)
	default:
		LogWindowEvent(win, "Unknown _NET_WM_STATE: "+prop)
	}
}

func (win *Window) toggleState(prop string) {
	switch prop {
	case "_NET_WM_STATE_FULLSCREEN":
		win.ToggleFullscreen()
	case "_NET_WM_STATE_MAXIMIZED_HORZ":
		win.ToggleMaximize(MaximizedH)
	case "_NET_WM_STATE_MAXIMIZED_VERT":
		win.ToggleMaximize(MaximizedV)
	case "_NET_WM_STATE_ABOVE":
		if win.Layer == LayerAbove {
			win.SetLayer(LayerNormal)
		} else {
			win.SetLayer(LayerAbove)
		}
	case "_NET_WM_STATE_BELOW":
		if win.Layer == LayerAbove {
			win.SetLayer(LayerNormal)
		} else {
			win.SetLayer(LayerBelow)
		}
	default:
		LogWindowEvent(win, "Unknown _NET_WM_STATE: "+prop)
	}
}

func (win *Window) SetLayer(layer Layer) {
	// TODO should we unexport the Layer field and provide a getter?
	win.Layer = layer
	win.updateWmState()
	windows := make(map[Layer][]*Window)
	for _, ow := range win.wm.GetWindows(icccm.StateNormal) {
		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		for _, ow := range windows[layer] {
			update = append(update, ow)
		}
	}
	win.wm.Restack(update)
}

func (win *Window) SendStructureNotify() {
	LogWindowEvent(win, "Sending StructureNotify")
	log.Printf("\tX: %d Y: %d W: %d H: %d", win.Geom.X, win.Geom.Y, win.Geom.Width, win.Geom.Height)
	ev := xproto.ConfigureNotifyEvent{
		Event:            win.Id,
		Window:           win.Id,
		AboveSibling:     xevent.NoWindow,
		X:                int16(win.Geom.X),
		Y:                int16(win.Geom.Y),
		Width:            uint16(win.Geom.Width),
		Height:           uint16(win.Geom.Height),
		BorderWidth:      1, // TODO settings
		OverrideRedirect: false,
	}
	xproto.SendEvent(win.wm.X.Conn(), false, win.Id,
		xproto.EventMaskStructureNotify, string(ev.Bytes()))
}

func (win *Window) Attributes() *xproto.GetWindowAttributesReply {
	attr, err := xproto.GetWindowAttributes(win.wm.X.Conn(), win.Id).Reply()
	if err != nil {
		return nil
	}
	return attr
}

func (win *Window) Class() (name string, class string) {
	repl, err := xprop.GetProperty(win.X, win.Id, "WM_CLASS")
	if err != nil {
		return "", ""
	}
	parts := strings.Split(string(repl.Value), "\x00")
	switch len(parts) {
	default:
		return parts[0], parts[1]
	case 1:
		return parts[0], ""
	case 0:
		return "", ""
	}
}

func (win *Window) Center() (x, y int) {
	return win.Geom.X + win.Geom.Width/2,
		win.Geom.Y + win.Geom.Height/2
}

func (win *Window) Screen() Geometry {
	screens := win.wm.Screens()
	cx, cy := win.Center()
	return screenForPoint(screens, cx, cy)
}

func (win *Window) updateWmState() {
	var atoms []string
	if (win.maximized & MaximizedH) > 0 {
		atoms = append(atoms, "_NET_WM_STATE_MAXIMIZED_HORZ")
	}
	if (win.maximized & MaximizedV) > 0 {
		atoms = append(atoms, "_NET_WM_STATE_MAXIMIZED_VERT")
	}
	if win.fullscreen {
		atoms = append(atoms, "_NET_WM_STATE_FULLSCREEN")
	}
	if win.Layer == LayerAbove {
		atoms = append(atoms, "_NET_WM_STATE_ABOVE")
	}
	if win.Layer == LayerBelow {
		atoms = append(atoms, "_NET_WM_STATE_BELOW")
	}
	// TODO other hints
	ewmh.WmStateSet(win.X, win.Id, atoms)
}

type WM struct {
	X         *xgbutil.XUtil
	Cursors   map[string]xproto.Cursor
	Root      *Window
	Config    *config.Config
	Windows   map[xproto.Window]*Window
	CurWindow *Window
	chFn      chan func()
}

func (wm *WM) MapRequest(xu *xgbutil.XUtil, ev xevent.MapRequestEvent) {
	win := wm.NewWindow(ev.Window)
	LogWindowEvent(win, "Mapping")
	if win.Mapped {
		LogWindowEvent(win, "Not mapping already mapped window")
		return
	}
	// TODO what's the point of the initial state? will an iconified window be mapped?

	hints, err := icccm.WmHintsGet(xu, win.Id)
	if err != nil {
		LogWindowEvent(win, "No WM_HINTS")
		hints = &icccm.Hints{}
	}
	win.Init()

	// FIXME if the window starts fullscreen make sure that X/Y is a
	// screen corner. wine's desktop doesn't set an X/Y at all (and
	// the usual mouse-pointer based calculation is wrong here), and
	// who knows what other broken clients are out there who do set a
	// non-sensical X/Y for a fullscreen client.

	normalHints, err := icccm.WmNormalHintsGet(xu, win.Id)
	if err != nil || (normalHints.Flags&(icccm.SizeHintPPosition|icccm.SizeHintUSPosition) == 0) {
		ptr, err := xproto.QueryPointer(wm.X.Conn(), wm.Root.Id).Reply()
		if err == nil {
			win.Geom.X = int(ptr.RootX) - win.Geom.Width/2
			win.Geom.Y = int(ptr.RootY) - win.Geom.Height/2
		} else {
			log.Println("Could not get pointer position:", err)
		}
	}

	win.moveNoReset()
	win.Map()
	// TODO probably should
	// a) store the border width in every client
	// b) use that for all calculations involving the border width
	win.CenterPointer()
	if (hints.Flags & icccm.HintState) == 0 {
		hints.InitialState = icccm.StateNormal
	}
	icccm.WmStateSet(wm.X, win.Id, &icccm.WmState{hints.InitialState, 0})
	win.State = State(hints.InitialState)

	win.SendStructureNotify()
	win.Mapped = true

	// Notes to self:
	// - x, y, w, h in WM_NORMAL_HINTS are obsolete
	// - we get the initial window geometry in (*Window).Init(), which
	//   reads the window's current geometry

	// FIXME make sure we get all the hints stuff right. i.e. set x/y/w/h if requested, call moveresize, etc
}

func (wm *WM) ConfigureRequest(xu *xgbutil.XUtil, ev xevent.ConfigureRequestEvent) {
	win := wm.NewWindow(ev.Window)
	LogWindowEvent(win, "Configure request")
	if win.curDrag != nil {
		LogWindowEvent(win, "Ignoring configure request because we are in a drag")
		return
	}

	m := ev.ValueMask

	if (m & xproto.ConfigWindowWidth) > 0 {
		win.Geom.Width = int(ev.Width)
	}
	if (m & xproto.ConfigWindowHeight) > 0 {
		win.Geom.Height = int(ev.Height)
	}
	if (m & xproto.ConfigWindowX) > 0 {
		win.Geom.X = int(ev.X)
	}
	if (m & xproto.ConfigWindowY) > 0 {
		win.Geom.Y = int(ev.Y)
	}

	// TODO stack order, border width, sibling

	win.Configure(int(ev.ValueMask) & ^(xproto.ConfigWindowSibling|xproto.ConfigWindowStackMode),
		win.Geom.X,
		win.Geom.Y,
		win.Geom.Width,
		win.Geom.Height,
		0,
		0,
	)

	win.SendStructureNotify()
}

func (wm *WM) NewWindow(c xproto.Window) *Window {
	if win, ok := wm.Windows[c]; ok {
		return win
	}

	win := &Window{wm: wm, Window: xwindow.New(wm.X, c)}
	LogWindowEvent(win, "Managing window")
	wm.Windows[c] = win

	attr := win.Attributes()
	if attr == nil {
		return win
	}

	switch attr.MapState {
	case xproto.MapStateUnmapped:
		// TODO how do we differentiate between withdrawn and iconified?
		win.State = icccm.StateWithdrawn
	case xproto.MapStateUnviewable, xproto.MapStateViewable:
		win.Mapped = true
		win.State = icccm.StateNormal
	}

	xevent.UnmapNotifyFun(win.UnmapNotify).Connect(win.wm.X, win.Id)
	xevent.DestroyNotifyFun(win.DestroyNotify).Connect(win.wm.X, win.Id)
	xevent.EnterNotifyFun(win.EnterNotify).Connect(win.wm.X, win.Id)
	xevent.ClientMessageFun(win.ClientMessage).Connect(win.wm.X, win.Id)

	return win
}

func (wm *WM) QueryTree() []xproto.Window {
	tree, err := xproto.QueryTree(wm.X.Conn(), wm.Root.Id).Reply()
	must(err)
	return tree.Children
}

func (wm *WM) RelevantQueryTree() []xproto.Window {
	tree := wm.QueryTree()
	var wins []xproto.Window
	for _, c := range tree {
		attr, err := xproto.GetWindowAttributes(wm.X.Conn(), c).Reply()
		must(err)
		if attr.OverrideRedirect || attr.MapState != xproto.MapStateViewable {
			continue
		}
		wins = append(wins, c)

	}
	return wins
}

func (wm *WM) GetWindows(states State) []*Window {
	if states == -1 {
		states = icccm.StateWithdrawn | icccm.StateIconic | icccm.StateNormal | icccm.StateInactive |
			icccm.StateZoomed
	}
	var windows []*Window
	for _, c := range wm.RelevantQueryTree() {
		win := wm.NewWindow(c)
		if win.State&states > 0 {
			windows = append(windows, win)
		}
	}
	return windows
}

func (wm *WM) Restack(windows []*Window) {
	if len(windows) < 2 {
		return
	}

	windows[0].StackSibling(windows[1].Id, xproto.StackModeBelow)
	for i := 2; i < len(windows); i++ {
		windows[i].StackSibling(windows[i-1].Id, xproto.StackModeAbove)
	}
}

func (wm *WM) Screens() []Geometry {
	heads, err := xinerama.PhysicalHeads(wm.X)
	if len(heads) == 0 || err != nil {
		rect, err := wm.Root.Geometry()
		must(err)
		heads = append(heads, rect)
	}
	geoms := make([]Geometry, len(heads))
	for i, h := range heads {
		geoms[i] = Geometry{X: h.X(), Y: h.Y(), Width: h.Width(), Height: h.Height()}
	}
	return geoms
}

func (wm *WM) LoadCursors(mapping map[string]uint16) {
	var err error
	for name, cursor := range mapping {
		wm.Cursors[name], err = xcursor.CreateCursor(wm.X, cursor)
		must(err)
	}
}

func (wm *WM) PointerPos() (x, y int) {
	ptr, err := xproto.QueryPointer(wm.X.Conn(), wm.Root.Id).Reply()
	if err != nil {
		log.Println("Could not query pointer position:", err)
		return 0, 0
	}
	return int(ptr.RootX), int(ptr.RootY)
}

func (wm *WM) CurrentScreen() Geometry {
	screens := wm.Screens()
	cx, cy := wm.PointerPos()
	return screenForPoint(screens, cx, cy)
}

func (wm *WM) debug() {
	log.Println("START DEBUG")
	log.Printf("- Managing %d windows", len(wm.Windows))
	log.Println("END DEBUG")
}

func (wm *WM) Restart() {
	log.Println("Restarting gwm")
	syscall.Exec(os.Args[0], os.Args, os.Environ())
}

func (wm *WM) windowSearchMenu() {
	wins := wm.GetWindows(icccm.StateNormal) // FIXME hidden windows
	var entries []menu.Entry
	for _, win := range wins {
		// ! currently focused
		// & hidden
		// XXX will need to fix this when we support hiding windows
		// XXX will need to fix this when we support groups
		entry := menu.Entry{Display: " " + win.Name(), Payload: win}
		entries = append(entries, entry)
	}
	filter := func(entries []menu.Entry, prompt string) []menu.Entry {
		const tiers = 4 // imitating cwm
		var outTiers [tiers][]menu.Entry
		prompt = strings.ToLower(prompt)
		for _, entry := range entries {
			win := entry.Payload.(*Window)
			tier := -1

			// TODO check by label
			// TODO check by old names
			if strings.Contains(strings.ToLower(win.Name()), prompt) {
				tier = 2
			} else {
				_, class := win.Class()
				if strings.Contains(strings.ToLower(class), prompt) {
					tier = 3
					entry.Display = " " + class + ":" + entry.Display[1:]
				}
			}

			if tier < 0 {
				continue
			}

			if win == wm.CurWindow {
				entry.Display = "!" + entry.Display[1:]
				if tier < tiers-1 {
					tier++
				}
			}

			// TODO rank one up if hidden
			outTiers[tier] = append(outTiers[tier], entry)
		}

		var out []menu.Entry
		for i := 0; i < tiers; i++ {
			out = append(out, outTiers[i]...)
		}
		return out
	}

	m := wm.newMenu("window", entries, filter)
	m.Show()
	go func() {
		if ret, ok := m.Wait(); ok && !ret.Synthetic() {
			wm.chFn <- func() {
				ret.Payload.(*Window).Activate()
			}
		}
	}()
}

func (wm *WM) newMenu(title string, entries []menu.Entry, filter menu.FilterFunc) *menu.Menu {
	px, py := wm.PointerPos()
	sc := subtractGaps(wm.CurrentScreen(), wm.Config.Gap)
	m := menu.New(wm.X, title, menu.Config{
		X:           px,
		Y:           py,
		MinY:        wm.Config.Gap.Top,
		MaxHeight:   sc.Height,
		BorderWidth: wm.Config.BorderWidth,
		BorderColor: wm.Config.Colors["activeborder"],
		FilterFn:    filter,
	})
	m.SetEntries(entries)
	return m
}

func (wm *WM) Init(xu *xgbutil.XUtil) {
	var err error
	wm.X = xu
	wm.LoadCursors(map[string]uint16{
		"fleur":               xcursor.Fleur,
		"normal":              xcursor.LeftPtr,
		"top_left_corner":     xcursor.TopLeftCorner,
		"top_right_corner":    xcursor.TopRightCorner,
		"bottom_left_corner":  xcursor.BottomLeftCorner,
		"bottom_right_corner": xcursor.BottomRightCorner,
	})

	mousebind.Initialize(wm.X)
	keybind.Initialize(wm.X)

	wm.Root = wm.NewWindow(wm.X.RootWin())
	xproto.ChangeWindowAttributes(wm.X.Conn(), wm.Root.Id, xproto.CwCursor,
		[]uint32{uint32(wm.Cursors["normal"])})
	var toMark *Window
	for _, w := range wm.RelevantQueryTree() {
		win := wm.NewWindow(w)
		win.Init()
		if win.ContainsPointer() {
			toMark = win
		}
	}

	if toMark != nil {
		toMark.markActive()
	}

	must(wm.Root.Listen(xproto.EventMaskStructureNotify, xproto.EventMaskSubstructureNotify,
		xproto.EventMaskFocusChange, xproto.EventMaskSubstructureRedirect))
	xevent.MapRequestFun(wm.MapRequest).Connect(xu, wm.Root.Id)
	xevent.ConfigureRequestFun(wm.ConfigureRequest).Connect(xu, wm.Root.Id)

	for key, cmd := range wm.Config.Binds {
		key, cmd := key, cmd
		should(keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
			if fn, ok := commands[cmd]; ok {
				fn(wm)
			} else {
				execute(cmd)
			}
		}).Connect(wm.X, wm.Root.Id, key.ToXGB(), true))
	}

	should(ewmh.NumberOfDesktopsSet(wm.X, 1))
	should(ewmh.CurrentDesktopSet(wm.X, 0))
	should(ewmh.DesktopViewportSet(wm.X, nil))
	should(ewmh.SupportedSet(wm.X, []string{
		"WM_TAKE_FOCUS",
		"_NET_ACTIVE_WINDOW",
		"_NET_WM_MOVERESIZE",
		"_NET_SUPPORTED",
		"_NET_NUMBER_OF_DESKTOPS",
		"_NET_CURRENT_DESKTOP",
		"_NET_SUPPORTING_WM_CHECK",
		"_NET_WM_NAME",
		"_NET_WM_STATE",
		"_NET_WM_STATE_MAXIMIZED_VERT",
		"_NET_WM_STATE_MAXIMIZED_HORZ",
		"_NET_WM_STATE_FULLSCREEN",
		"_NET_WM_ALLOWED_ACTIONS",
		"_NET_WM_ACTION_FULLSCREEN",
		"_NET_WM_ACTION_MAXIMIZE_VERT",
		"_NET_WM_ACTION_MAXIMIZE_HORZ",
	}))

	win, err := xwindow.Create(wm.X, wm.Root.Id)
	must(err)
	must(ewmh.SupportingWmCheckSet(wm.X, wm.Root.Id, win.Id))
	must(ewmh.WmNameSet(wm.X, win.Id, "gwm"))

	before, after, quit := xevent.MainPing(wm.X)
	for {
		select {
		case <-before:
			<-after
		case fn := <-wm.chFn:
			fn()
		case <-quit:
			return
		}
	}
}

func main() {
	log.Println("Starting gwm")
	p := "./cwmrc"
	if len(os.Args) > 1 {
		p = os.Args[1]
	}
	f, _ := os.Open(p)
	cfg, err := config.Parse(f)
	if err != nil {
		panic(err)
	}
	wm := &WM{
		Config: cfg,
		// FIXME all of the make() stuff should be in the Init() method
		Cursors: make(map[string]xproto.Cursor),
		Windows: make(map[xproto.Window]*Window),
		chFn:    make(chan func()),
	}
	xu, err := xgbutil.NewConn()
	must(err)
	wm.Init(xu)
}

func execute(bin string) error {
	cmd := exec.Command("/bin/sh", "-c", bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Start()
	if err != nil {
		log.Printf("Could not execute %q: %s", bin, err)
		return err
	}
	go func() { cmd.Process.Wait() }()
	return nil
}

func winmovefunc(xf, yf int) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		win := wm.CurWindow
		win.Move(win.Geom.X+xf*wm.Config.MoveAmount, win.Geom.Y+yf*wm.Config.MoveAmount)
		// TODO apply snapping
	}
}

func winmaximizefunc(state MaximizedState) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		wm.CurWindow.ToggleMaximize(state)
	}
}

func winfunc(fn func(*Window)) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		fn(wm.CurWindow)
	}
}

func winlayerfunc(layer Layer) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		win := wm.CurWindow
		if win.Layer == layer {
			win.SetLayer(LayerNormal)
		} else {
			win.SetLayer(layer)
		}
	}
}

var commands = map[string]func(wm *WM){
	"lower":        winfunc((*Window).Lower),
	"raise":        winfunc((*Window).Raise),
	"moveup":       winmovefunc(0, -1),
	"bigmoveup":    winmovefunc(0, -10),
	"movedown":     winmovefunc(0, 1),
	"bigmovedown":  winmovefunc(0, 10),
	"moveleft":     winmovefunc(-1, 0),
	"bigmoveleft":  winmovefunc(-10, 0),
	"moveright":    winmovefunc(1, 0),
	"bigmoveright": winmovefunc(10, 0),
	"maximize":     winmaximizefunc(MaximizedFull),
	"vmaximize":    winmaximizefunc(MaximizedV),
	"hmaximize":    winmaximizefunc(MaximizedH),
	"fullscreen":   winfunc((*Window).ToggleFullscreen),
	"freeze":       winfunc((*Window).ToggleFreeze),
	"above":        winlayerfunc(LayerAbove),
	"below":        winlayerfunc(LayerBelow),
	"delete":       winfunc((*Window).Delete),

	"debug":   (*WM).debug,
	"restart": (*WM).Restart,

	"terminal": func(wm *WM) {
		if cmd, ok := wm.Config.Commands["term"]; ok {
			execute(cmd)
		}
	},

	"exec": func(wm *WM) {
		entries := executables()
		m := wm.newMenu("exec", entries, menu.FilterPrefix)
		m.Show()
		go func() {
			// XXX make sure execute() is thread-safe
			if ret, ok := m.Wait(); ok {
				log.Println("Executing", ret.Payload.(string))
				execute(ret.Payload.(string))
			}
		}()
	},

	"search": (*WM).windowSearchMenu,
}

// TODO watch for wm_normal_hints changes
// TODO honor aspect ratio when resizing
// TODO remove wm_state when withdrawing
// TODO unset _NET_DESKTOP_NAMES
// TODO set allowed actions
