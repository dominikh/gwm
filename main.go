package main

import (
	"log"
	"os"
	"os/exec"
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
	"github.com/BurntSushi/xgbutil/xrect"
	"github.com/BurntSushi/xgbutil/xwindow"

	"honnef.co/go/gwm/config"
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

func subtractGaps(sc xrect.Rect, gap config.Gap) xrect.Rect {
	// Copy into a new xrect.Rect
	out := xrect.New(sc.Pieces())
	out.XSet(out.X() + gap.Left)
	out.YSet(out.Y() + gap.Top)
	out.WidthSet(out.Width() - gap.Left - gap.Right)
	out.HeightSet(out.Height() - gap.Top - gap.Bottom)
	return out
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

type Screen struct {
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

type geom struct {
	X, Y          int
	Width, Height int
}
type Window struct {
	*xwindow.Window
	State       State
	Layer       Layer
	Mapped      bool
	Geom        geom
	BorderWidth int
	wm          *WM
	curDrag     drag
}

func (w *Window) Name() string {
	name, err := ewmh.WmNameGet(w.wm.X, w.Id)
	if name == "" || err != nil {
		name, _ = icccm.WmNameGet(w.wm.X, w.Id)
	}

	return name
}

func (w *Window) SetBorderColor(color int) {
	w.Change(xproto.CwBorderPixel, uint32(color))
}

func (w *Window) SetBorderWidth(width int) {
	w.BorderWidth = width
	xproto.ConfigureWindow(w.wm.X.Conn(), w.Id, xproto.ConfigWindowBorderWidth, []uint32{uint32(width)})
}

func (w *Window) Raise() {
	windows := make(map[Layer][]*Window)
	for _, ow := range w.wm.GetWindows(icccm.StateNormal) {
		if ow.Id == w.Id || ow.Id == w.wm.Root.Id {
			continue
		}

		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	windows[w.Layer] = append(windows[w.Layer], w)

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		for _, ow := range windows[layer] {
			update = append(update, ow)
		}
	}
	w.wm.Restack(update)
}

func (w *Window) Lower() {
	windows := make(map[Layer][]*Window)
	windows[w.Layer] = []*Window{w}
	for _, ow := range w.wm.GetWindows(icccm.StateNormal) {
		if ow.Id == w.Id || ow.Id == w.wm.Root.Id {
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
	w.wm.Restack(update)
}

func (w *Window) MoveBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
	w.curDrag = drag{w.Geom.X, w.Geom.Y, rootX, rootY, cornerNone}
	w.Raise()
	return true, w.wm.Cursors["fleur"]
}

func (w *Window) MoveStep(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	dx := rootX - w.curDrag.offsetX
	dy := rootY - w.curDrag.offsetY

	// FIXME do we need to consider the border here?
	w.Geom.X = w.curDrag.startX + dx
	w.Geom.Y = w.curDrag.startY + dy

	screen := w.Screen()
	screen = subtractGaps(screen, w.wm.Config.Gap)

	if screen == nil {
		LogWindowEvent(w, "Could not determine screen for window")
	} else {
		w.Geom.X += snapcalc(w.Geom.X, w.Geom.X+w.Geom.Width+w.BorderWidth*2,
			screen.X(), screen.X()+screen.Width(), w.wm.Config.Snapdist)
		w.Geom.Y += snapcalc(w.Geom.Y, w.Geom.Y+w.Geom.Height+w.BorderWidth*2,
			screen.Y(), screen.Y()+screen.Height(), w.wm.Config.Snapdist)
	}
	w.move()
}

func (w *Window) MoveEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
}

func (w *Window) ResizeBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
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

	if eventX > w.Geom.Width/2 {
		corner |= cornerE
		cursorX = "right"
		x = w.Geom.Width
	} else {
		corner |= cornerW
		cursorX = "left"
	}

	if eventY > w.Geom.Height/2 {
		corner |= cornerS
		cursorY = "bottom"
		y = w.Geom.Height
	} else {
		corner |= cornerN
		cursorY = "top"
	}

	w.curDrag = drag{w.Geom.X, w.Geom.Y, rootX, rootY, corner}
	xproto.WarpPointer(w.wm.X.Conn(), xproto.WindowNone, w.Id, 0, 0, 0, 0, int16(x), int16(y))
	return true, w.wm.Cursors[cursorY+"_"+cursorX+"_corner"]
}

func (w *Window) ResizeStep(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	// FIXME consider size hints
	if (w.curDrag.corner & cornerW) > 0 {
		w.Geom.Width += w.Geom.X - rootX + w.wm.Config.BorderWidth
		w.Geom.X = rootX - w.wm.Config.BorderWidth
	}

	if (w.curDrag.corner & cornerE) > 0 {
		w.Geom.Width += rootX - (w.Geom.X + w.Geom.Width + w.wm.Config.BorderWidth)
	}

	if (w.curDrag.corner & cornerS) > 0 {
		w.Geom.Height += rootY - (w.Geom.Y + w.Geom.Height + w.wm.Config.BorderWidth)
	}

	if (w.curDrag.corner & cornerN) > 0 {
		w.Geom.Height += w.Geom.Y - rootY + w.wm.Config.BorderWidth
		w.Geom.Y = rootY - w.wm.Config.BorderWidth
	}

	w.moveAndResize()
}

func (w *Window) ResizeEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
}

func (w *Window) Move(x, y int) {
	w.Geom.X = x
	w.Geom.Y = y
	w.move()
}

func (w *Window) MoveAndResize(x, y, width, height int) {
	w.Geom.X = x
	w.Geom.Y = y
	w.Geom.Width = width
	w.Geom.Height = height
	w.moveAndResize()
}

// move moves the window based on its current Geom.
func (w *Window) move() {
	w.Window.Move(w.Geom.X, w.Geom.Y)
}

// moveAndResize moves and resizes the window based on its current Geom.
func (w *Window) moveAndResize() {
	w.Window.MoveResize(w.Geom.X, w.Geom.Y, w.Geom.Width, w.Geom.Height)
}

func (w *Window) EnterNotify(xu *xgbutil.XUtil, ev xevent.EnterNotifyEvent) {
	LogWindowEvent(w, "Enter")
	if w == w.wm.CurWindow {
		return
	}
	if !w.Focusable() {
		LogWindowEvent(w, "\tnot focusable, skipping")
		return
	}
	w.SetBorderColor(w.wm.Config.Colors["activeborder"])
	w.Focus()
	if curwin := w.wm.CurWindow; curwin != nil {
		curwin.SetBorderColor(w.wm.Config.Colors["inactiveborder"])
	}
	w.wm.CurWindow = w
}

func (w *Window) Focus() {
	w.Window.Focus()
	should(ewmh.ActiveWindowSet(w.wm.X, w.Id))
}

func (w *Window) Focusable() bool {
	hints, err := icccm.WmHintsGet(w.wm.X, w.Id)
	if err != nil {
		LogWindowEvent(w, "Could not read hints")
		return true
	}
	return hints.Input == 1
}

func (w *Window) DestroyNotify(xu *xgbutil.XUtil, ev xevent.DestroyNotifyEvent) {
	LogWindowEvent(w, "Destroying")
	w.Detach()
	delete(w.wm.Windows, w.Id)
}

func (w *Window) UnmapNotify(xu *xgbutil.XUtil, ev xevent.UnmapNotifyEvent) {
	LogWindowEvent(w, "Unmapping")
	w.Mapped = false
	w.Detach()
	w.State = icccm.StateIconic
	should(icccm.WmStateSet(w.wm.X, w.Id, &icccm.WmState{State: uint(w.State)}))
}

func (w *Window) Init() {
	// TODO do something if the state is iconified
	// TODO set the window's layer
	LogWindowEvent(w, "Initializing")
	should(w.Listen(xproto.EventMaskEnterWindow, xproto.EventMaskFocusChange, xproto.EventMaskStructureNotify, xproto.EventMaskPointerMotion))
	w.SetBorderWidth(w.wm.Config.BorderWidth)
	w.SetBorderColor(w.wm.Config.Colors["inactiveborder"])

	attr, err := xproto.GetGeometry(w.wm.X.Conn(), xproto.Drawable(w.Id)).Reply()
	if err != nil {
		should(err)
	} else {
		w.Geom.X = int(attr.X)
		w.Geom.Y = int(attr.Y)
		w.Geom.Width = int(attr.Width)
		w.Geom.Height = int(attr.Height)
	}

	if ms, ok := w.wm.Config.MouseBinds["window_move"]; ok {
		mousebind.Drag(w.wm.X, w.Id, w.Id, ms.ToXGB(), true, w.MoveBegin, w.MoveStep, w.MoveEnd)
	}

	if ms, ok := w.wm.Config.MouseBinds["window_resize"]; ok {
		mousebind.Drag(w.wm.X, w.Id, w.Id, ms.ToXGB(), true, w.ResizeBegin, w.ResizeStep, w.ResizeEnd)
	}

	if ms, ok := w.wm.Config.MouseBinds["window_lower"]; ok {
		fn := func(xu *xgbutil.XUtil, ev xevent.ButtonPressEvent) { w.Lower() }
		should(mousebind.ButtonPressFun(fn).Connect(w.wm.X, w.Id, ms.ToXGB(), false, true))
	}

	xevent.UnmapNotifyFun(w.UnmapNotify).Connect(w.wm.X, w.Id)
	xevent.DestroyNotifyFun(w.DestroyNotify).Connect(w.wm.X, w.Id)
	xevent.EnterNotifyFun(w.EnterNotify).Connect(w.wm.X, w.Id)

	should(icccm.WmStateSet(w.wm.X, w.Id, &icccm.WmState{State: uint(w.State)}))
}

type WM struct {
	X         *xgbutil.XUtil
	Cursors   map[string]xproto.Cursor
	Root      *Window
	Config    *config.Config
	Windows   map[xproto.Window]*Window
	CurWindow *Window
}

func (wm *WM) MapRequest(xu *xgbutil.XUtil, ev xevent.MapRequestEvent) {
	win := wm.GetWindow(ev.Window)
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
	// FIXME set x and y to pointer position only if the app didn't set USPosition/PPosition
	ptr, err := xproto.QueryPointer(wm.X.Conn(), wm.Root.Id).Reply()
	if err == nil {
		win.Geom.X = int(ptr.RootX) - win.Geom.Width/2
		win.Geom.Y = int(ptr.RootY) - win.Geom.Height/2
	} else {
		log.Println("Could not get pointer position:", err)
	}
	win.move()
	win.Map()
	if (hints.Flags & icccm.HintState) > 0 {
		win.State = State(hints.InitialState)
	} else {
		win.State = icccm.StateNormal
	}
	win.SendStructureNotify()
	win.Mapped = true

	// Notes to self:
	// - x, y, w, h in WM_NORMAL_HINTS are obsolete
	// - we get the initial window geometry in (*Window).Init(), which
	//   reads the window's current geometry

	// FIXME make sure we get all the hints stuff right. i.e. set x/y/w/h if requested, call moveresize, etc
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

func (wm *WM) ConfigureRequest(xu *xgbutil.XUtil, ev xevent.ConfigureRequestEvent) {
	win := wm.GetWindow(ev.Window)
	LogWindowEvent(win, ev.ValueMask)
	LogWindowEvent(win, "Configure request")

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

	win.Configure(int(ev.ValueMask),
		win.Geom.X,
		win.Geom.Y,
		win.Geom.Width,
		win.Geom.Height,
		0,
		0,
	)

	win.SendStructureNotify()
}

func (w *Window) SendStructureNotify() {
	LogWindowEvent(w, "Sending StructureNotify")
	log.Printf("\tX: %d Y: %d W: %d H: %d", w.Geom.X, w.Geom.Y, w.Geom.Width, w.Geom.Height)
	ev := xproto.ConfigureNotifyEvent{
		Event:            w.Id,
		Window:           w.Id,
		AboveSibling:     xevent.NoWindow,
		X:                int16(w.Geom.X),
		Y:                int16(w.Geom.Y),
		Width:            uint16(w.Geom.Width),
		Height:           uint16(w.Geom.Height),
		BorderWidth:      1, // TODO settings
		OverrideRedirect: false,
	}
	xproto.SendEvent(w.wm.X.Conn(), false, w.Id,
		xproto.EventMaskStructureNotify, string(ev.Bytes()))
}

func (wm *WM) CreateNotify(xu *xgbutil.XUtil, ev xevent.CreateNotifyEvent) {
	win := wm.NewWindow(ev.Window)
	LogWindowEvent(win, "Created new window")
}

func (w *Window) Attributes() *xproto.GetWindowAttributesReply {
	attr, err := xproto.GetWindowAttributes(w.wm.X.Conn(), w.Id).Reply()
	if err != nil {
		return nil
	}
	return attr
}

func (wm *WM) NewWindow(c xproto.Window) *Window {
	// Just for extra security
	if win, ok := wm.Windows[c]; ok {
		LogWindowEvent(win, "NewWindow called for the same window twice")
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
	return win
}

func (wm *WM) GetWindow(c xproto.Window) *Window {
	return wm.Windows[c]
}

func (wm *WM) QueryTree() []xproto.Window {
	tree, err := xproto.QueryTree(wm.X.Conn(), wm.Root.Id).Reply()
	must(err)
	return tree.Children
}

func (wm *WM) GetWindows(states State) []*Window {
	if states == -1 {
		states = icccm.StateWithdrawn | icccm.StateIconic | icccm.StateNormal | icccm.StateInactive | icccm.StateZoomed
	}
	var windows []*Window
	for _, c := range wm.QueryTree() {
		win := wm.GetWindow(c)
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

func (wm *WM) Screens() []xrect.Rect {
	heads, err := xinerama.PhysicalHeads(wm.X)
	if len(heads) == 0 || err != nil {
		rect, err := wm.Root.Geometry()
		must(err)
		heads = append(heads, rect)
	}
	return heads
}

func (w *Window) Center() (x, y int) {
	return w.Geom.X + w.Geom.Width/2,
		w.Geom.Y + w.Geom.Height/2
}

func (w *Window) Screen() xrect.Rect {
	screens := w.wm.Screens()
	cx, cy := w.Center()
	var screen xrect.Rect
	for _, screen = range screens {
		if (cx >= screen.X() && cx <= screen.X()+screen.Width()) &&
			(cy >= screen.Y() && cy <= screen.Y()+screen.Height()) {
			return screen
		}
	}

	return screen
}

func (wm *WM) LoadCursors(mapping map[string]uint16) {
	var err error
	for name, cursor := range mapping {
		wm.Cursors[name], err = xcursor.CreateCursor(wm.X, cursor)
		must(err)
	}
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
	xproto.ChangeWindowAttributes(wm.X.Conn(), wm.Root.Id, xproto.CwCursor, []uint32{uint32(wm.Cursors["normal"])})
	for _, w := range wm.QueryTree() {
		win := wm.NewWindow(w)
		if win.State&(icccm.StateNormal|icccm.StateIconic) > 0 && !win.Attributes().OverrideRedirect {
			// FIXME note initial geometry for existing clients
			win.Init()
		}
	}

	must(wm.Root.Listen(xproto.EventMaskStructureNotify, xproto.EventMaskSubstructureNotify, xproto.EventMaskFocusChange, xproto.EventMaskSubstructureRedirect))
	xevent.MapRequestFun(wm.MapRequest).Connect(xu, wm.Root.Id)
	xevent.ConfigureRequestFun(wm.ConfigureRequest).Connect(xu, wm.Root.Id)
	xevent.CreateNotifyFun(wm.CreateNotify).Connect(xu, wm.Root.Id)

	for key, cmd := range wm.Config.Binds {
		key, cmd := key, cmd
		should(keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
			if fn, ok := commands[cmd]; ok {
				fn(wm, ev)
			} else {
				execute(cmd)
			}
		}).Connect(wm.X, wm.Root.Id, key.ToXGB(), true))
	}

	should(ewmh.NumberOfDesktopsSet(wm.X, 1))
	should(ewmh.CurrentDesktopSet(wm.X, 0))
	should(ewmh.DesktopViewportSet(wm.X, nil))

	win, err := xwindow.Create(wm.X, wm.Root.Id)
	must(err)
	must(ewmh.SupportingWmCheckSet(wm.X, wm.Root.Id, win.Id))
	must(ewmh.WmNameSet(wm.X, win.Id, "gwm"))

	xevent.Main(wm.X)
}

func main() {
	f, _ := os.Open("./cwmrc")
	cfg, err := config.Parse(f)
	if err != nil {
		panic(err)
	}
	wm := &WM{
		Config:  cfg,
		Cursors: make(map[string]xproto.Cursor),
		Windows: make(map[xproto.Window]*Window),
	}
	xu, err := xgbutil.NewConn()
	must(err)
	wm.Init(xu)
}

func execute(bin string) error {
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Start()
	if err != nil {
		log.Printf("Could not execute %q", bin)
		return err
	}
	cmd.Process.Release()
	return nil
}

func winmovefunc(xf, yf int) func(*WM, xevent.KeyPressEvent) {
	return func(wm *WM, ev xevent.KeyPressEvent) {
		if wm.CurWindow == nil {
			return
		}
		win := wm.CurWindow
		win.Move(win.Geom.X+xf*wm.Config.MoveAmount, win.Geom.Y+yf*wm.Config.MoveAmount)
		// TODO apply snapping
	}
}

func winfunc(fn func(*Window)) func(*WM, xevent.KeyPressEvent) {
	return func(wm *WM, ev xevent.KeyPressEvent) {
		if wm.CurWindow == nil {
			return
		}
		fn(wm.CurWindow)
	}
}

var commands = map[string]func(wm *WM, ev xevent.KeyPressEvent){
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
	"terminal": func(wm *WM, ev xevent.KeyPressEvent) {
		if cmd, ok := wm.Config.Commands["term"]; ok {
			execute(cmd)
		}
	},
}
