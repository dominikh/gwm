package main

/*
Notes

This is a list of things that I don't want to forget because they
tripped me up:

- xgb/xgbutil seems to dispatch ClientMessage events to the window it
  was targetted at, not the root window it was actually sent to

*/
import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/xgb/shape"
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
	"honnef.co/go/gwm/draw"
	"honnef.co/go/gwm/internal/quadtree"
	"honnef.co/go/gwm/menu"
)

func abs(x int) int {
	if x >= 0 {
		return x
	}

	return -x
}

func roundDown(num int, multiple int) int {
	if multiple == 0 {
		return num
	}
	return num - (num % multiple)
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
		}
		return s1
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

	var last string
	entries := make([]menu.Entry, 0, len(executables))
	for _, e := range executables {
		if e == last {
			continue
		}
		last = e
		entries = append(entries, menu.Entry{Display: e, Payload: e})
	}
	return entries
}

func screenForPoint(screens []Geometry, p Point) Geometry {
	var screen Geometry
	for _, screen = range screens {
		if screen.Contains(p) {
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
	pointer Point
	start   Point
	offset  Point
	corner  corner
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
	Fullscreen    MaximizedState = 4
)

type Point struct {
	X int
	Y int
}

type Geometry struct {
	X, Y          int
	Width, Height int
}

func (g Geometry) subtractGap(gap config.Gap) Geometry {
	g.X += gap.Left
	g.Y += gap.Top
	g.Width -= gap.Left + gap.Right
	g.Height -= gap.Top + gap.Bottom
	return g
}

func (g Geometry) Contains(p Point) bool {
	return !(p.X < g.X || p.X > g.X+g.Width || p.Y < g.Y || p.Y > g.Y+g.Height)
}

type Window struct {
	*xwindow.Window
	State             State
	Layer             Layer
	Layout            Layout
	LayoutStack       []Layout
	Mapped            bool
	BorderWidth       int
	wm                *WM
	curDrag           *drag
	unfullscreenGeom  Geometry
	unfullscreenLayer Layer
	frozen            bool
	overlay           *Window
	gcs               draw.GCs
}

func (win *Window) GCs() draw.GCs {
	return win.gcs
}

func (win *Window) Win() xproto.Window {
	return win.Id
}

func (win *Window) X() *xgbutil.XUtil {
	return win.wm.X
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

func (win *Window) SetName(name string) {
	if err := ewmh.WmNameSet(win.X(), win.Window.Id, name); err != nil {
		icccm.WmNameSet(win.X(), win.Window.Id, name)
	}
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
	for _, ow := range win.wm.MappedWindows() {
		if ow.Id == win.Id || ow.Id == win.wm.Root.Id {
			continue
		}

		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	windows[win.Layer] = append(windows[win.Layer], win)

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		update = append(update, windows[layer]...)
	}
	win.wm.Restack(update)
}

func (win *Window) Lower() {
	windows := make(map[Layer][]*Window)
	windows[win.Layer] = []*Window{win}
	for _, ow := range win.wm.MappedWindows() {
		if ow.Id == win.Id || ow.Id == win.wm.Root.Id {
			continue
		}

		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		update = append(update, windows[layer]...)
	}
	win.wm.Restack(update)
}

func (win *Window) MoveBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
	win.PushLayout()
	win.Raise()
	win.curDrag = &drag{
		pointer: win.wm.PointerPos(),
		start:   Point{win.Layout.X, win.Layout.Y},
		offset:  Point{rootX, rootY},
		corner:  cornerNone,
	}
	return true, win.wm.Cursors["fleur"]
}

func (win *Window) MoveStep(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	if win.frozen {
		return
	}
	dx := rootX - win.curDrag.offset.X
	dy := rootY - win.curDrag.offset.Y

	// FIXME do we need to consider the border here?
	win.Layout.X = win.curDrag.start.X + dx
	win.Layout.Y = win.curDrag.start.Y + dy

	screen := win.Screen()
	screen = screen.subtractGap(win.wm.Config.Gap)

	win.Layout.X += snapcalc(win.Layout.X, win.Layout.X+win.Layout.Width+win.BorderWidth*2,
		screen.X, screen.X+screen.Width, win.wm.Config.Snapdist)
	win.Layout.Y += snapcalc(win.Layout.Y, win.Layout.Y+win.Layout.Height+win.BorderWidth*2,
		screen.Y, screen.Y+screen.Height, win.wm.Config.Snapdist)
	win.move()
}

func (win *Window) MoveEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	win.curDrag = nil
}

func (win *Window) ResizeBegin(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) (bool, xproto.Cursor) {
	win.PushLayout()

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

	if eventX > win.Layout.Width/2 {
		corner |= cornerE
		cursorX = "right"
		x = win.Layout.Width
	} else {
		corner |= cornerW
		cursorX = "left"
	}

	if eventY > win.Layout.Height/2 {
		corner |= cornerS
		cursorY = "bottom"
		y = win.Layout.Height
	} else {
		corner |= cornerN
		cursorY = "top"
	}

	win.curDrag = &drag{
		pointer: win.wm.PointerPos(),
		start:   Point{win.Layout.X, win.Layout.Y},
		offset:  Point{rootX, rootY},
		corner:  corner,
	}

	// TODO move WarpPointer to method on Window
	xproto.WarpPointer(win.wm.X.Conn(), xproto.WindowNone, win.Id, 0, 0, 0, 0, int16(x), int16(y))
	win.ShowOverlay()
	return true, win.wm.Cursors[cursorY+"_"+cursorX+"_corner"]
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
		dw = win.Layout.X - rootX
		dw = roundDown(dw, wInc)
		dx = -dw
	}

	if (win.curDrag.corner & cornerE) > 0 {
		dw = rootX - (win.Layout.X + win.Layout.Width)
		dw = roundDown(dw, wInc)
	}

	if (win.curDrag.corner & cornerS) > 0 {
		dh = rootY - (win.Layout.Y + win.Layout.Height)
		dh = roundDown(dh, hInc)
	}

	if (win.curDrag.corner & cornerN) > 0 {
		dh = win.Layout.Y - rootY
		dh = roundDown(dh, hInc)
		dy = -dh
	}

	nh := win.Layout.Height + dh
	nw := win.Layout.Width + dw

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
			dx -= nw - (win.Layout.Width + dw)
		}

		nw += wBase
		nh += hBase
	}

	if nh >= hMin && (!hasMax || nh <= hMax) {
		win.Layout.Height = nh
		win.Layout.Y += dy
	}

	if nw >= wMin && (!hasMax || nw <= wMax) {
		win.Layout.Width = nw
		win.Layout.X += dx
	}

	win.moveAndResize()
	win.WriteToOverlay(fmt.Sprintf("%d × %d", win.Layout.Width, win.Layout.Height))
}

func (win *Window) ResizeEnd(xu *xgbutil.XUtil, rootX, rootY, eventX, eventY int) {
	win.HideOverlay()
	if win.Layout.Contains(win.curDrag.pointer) {
		win.wm.WarpPointer(win.curDrag.pointer)
	} else if !win.Layout.Contains(Point{rootX, rootY}) {
		win.CenterPointer()
	}
	win.curDrag = nil
}

func (win *Window) Move(x, y int) {
	// TODO document that this function will reset the maximized state
	win.Layout.X = x
	win.Layout.Y = y
	win.move()
}

func (win *Window) Resize(w, h int) {
	win.Layout.Width = w
	win.Layout.Height = h
	win.resize()
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

func (win *Window) PushLayout() {
	if len(win.LayoutStack) > 0 && win.LayoutStack[len(win.LayoutStack)-1] == win.Layout {
		return
	}
	win.LayoutStack = append(win.LayoutStack, win.Layout)
	if len(win.LayoutStack) > 10 {
		copy(win.LayoutStack, win.LayoutStack[1:])
		win.LayoutStack = win.LayoutStack[:len(win.LayoutStack)-1]
	}
}

func (win *Window) PopLayout() {
	if len(win.LayoutStack) == 0 {
		return
	}
	l := win.LayoutStack[len(win.LayoutStack)-1]
	win.LayoutStack = win.LayoutStack[:len(win.LayoutStack)-1]
	win.ApplyLayout(l)
	win.CenterPointer()
}

func (win *Window) ApplyLayout(l Layout) {
	win.Layout = l
	win.moveAndResizeNoReset()
	win.updateWmState()
}

func (win *Window) Fullscreen() {
	if win.Layout.State == Fullscreen {
		return
	}

	// TODO what about min/max size and increments?

	sc := win.Screen()
	win.unfullscreenGeom = win.Layout.Geometry
	win.SetBorderWidth(0)
	win.Layout.Geometry = sc
	win.moveAndResizeNoReset()
	win.Layout.State = Fullscreen
	win.Freeze()
	win.unfullscreenLayer = win.Layer
	win.SetLayer(LayerAbove)
	win.Raise()
	win.updateWmState()
}

func (win *Window) Unfullscreen() {

	if win.Layout.State != Fullscreen {
		return
	}

	win.Layout.Geometry = win.unfullscreenGeom
	win.SetBorderWidth(win.wm.Config.BorderWidth)
	win.moveAndResizeNoReset()
	win.Layout.State = 0
	win.Unfreeze()
	win.SetLayer(win.unfullscreenLayer)
	win.updateWmState()
}

func (win *Window) ToggleFullscreen() {
	if win.Layout.State == Fullscreen {
		win.Unfullscreen()
	} else {
		win.Fullscreen()
	}
}

func (win *Window) Corners() (Point, Point, Point, Point) {
	bw := win.BorderWidth
	p1 := Point{win.Layout.X - bw, win.Layout.Y - bw}
	p2 := Point{win.Layout.X + win.Layout.Width + bw, win.Layout.Y - bw}
	p3 := Point{win.Layout.X + win.Layout.Width + bw, win.Layout.Y + win.Layout.Height + bw}
	p4 := Point{win.Layout.X - bw, win.Layout.Y + win.Layout.Height + bw}

	return p1, p2, p3, p4
}

func (win *Window) Overlaps(other *Window) bool {
	p1, p2, p3, _ := win.Corners()
	op1, op2, op3, _ := other.Corners()

	return (p3.Y <= op1.Y || p1.Y >= op3.Y) &&
		(p2.X <= op1.X || p1.X >= op2.X)
}

func (wm *WM) MappedWindows() []*Window {
	return wm.GetWindows(icccm.StateNormal)
}

func (wm *WM) VisibleWindows() []*Window {
	wins := wm.MappedWindows()
	q := quadtree.New(3840) // XXX get screen size
	for _, win := range wins {
		q.SetRegion(quadtree.Region{
			X:      win.Layout.X,
			Y:      win.Layout.Y,
			Width:  win.Layout.Width,
			Height: win.Layout.Height,
		}, int(win.Id))
	}

	var out []*Window
	for _, win := range wins {
		if q.HasValue(quadtree.Region{
			X:      win.Layout.X,
			Y:      win.Layout.Y,
			Width:  win.Layout.Width,
			Height: win.Layout.Height,
		}, int(win.Id)) {
			out = append(out, win)
		}
	}
	return out
}

func (win *Window) collisions(with []*Window) (left, top, right, bottom int) {
	// FIXME what happens with windows that span screens?
	screen := win.Screen()
	screen = screen.subtractGap(win.wm.Config.Gap)

	p1, p2, p3, _ := win.Corners()
	bw := win.BorderWidth
	left = screen.X + 2*bw
	top = screen.Y + 2*bw
	right = screen.X + screen.Width - 2*bw
	bottom = screen.Y + screen.Height - 2*bw
	for _, owin := range with {
		if win.Id == owin.Id {
			continue
		}
		if win.Overlaps(owin) {
			continue
		}

		op1, op2, op3, _ := owin.Corners()
		if op1.Y < p1.Y && op1.Y < p3.Y && op3.Y < p1.Y && op3.Y < p3.Y {
			continue
		}
		if op1.Y > p1.Y && op1.Y > p3.Y && op3.Y > p1.Y && op3.Y > p3.Y {
			continue
		}
		if op2.X <= p1.X && op2.X > left {
			left = op2.X + bw
		}
		if op1.X >= p2.X && op1.X < right {
			right = op1.X - bw
		}
	}
	for _, owin := range with {
		if win.Id == owin.Id {
			continue
		}
		if win.Overlaps(owin) {
			continue
		}

		op1, op2, op3, _ := owin.Corners()

		if op1.X < p1.X && op1.X < p2.X && op2.X < p1.X && op2.X < p2.X {
			continue
		}
		if op1.X > p1.X && op1.X > p2.X && op2.X > p1.X && op2.X > p2.X {
			continue
		}
		if op3.Y <= p1.Y && op3.Y > top {
			top = op3.Y + bw
		}

		if op1.Y >= p3.Y && op1.Y < bottom {
			bottom = op1.Y - bw
		}
	}
	return left, top, right, bottom
}

type Rectangle struct {
	bw  uint16
	bc  int
	wm  *WM
	win *xwindow.Window

	lastPos [4]int
}

func (wm *WM) NewRectangle(borderWidth uint16, borderColor int) (*Rectangle, error) {
	ov, err := xwindow.Generate(wm.X)
	if err != nil {
		return nil, err
	}
	if err := ov.CreateChecked(wm.Root.Id, 0, 0, 1, 1, xproto.CwBackPixel, uint32(borderColor)); err != nil {
		ov.Destroy()
		return nil, err
	}
	xproto.ConfigureWindow(wm.X.Conn(), ov.Id, xproto.ConfigWindowBorderWidth, []uint32{uint32(0)})
	shape.Rectangles(wm.X.Conn(), shape.SoSet, shape.SkBounding, 0, ov.Id, 0, 0, []xproto.Rectangle{})
	return &Rectangle{
		bw:  borderWidth,
		bc:  borderColor,
		wm:  wm,
		win: ov,
	}, nil
}

func (r *Rectangle) Id() xproto.Window {
	return r.win.Id
}

func (r *Rectangle) Show() {
	r.win.Map()
}

func (r *Rectangle) Destroy() {
	r.win.Destroy()
}

func (r *Rectangle) MoveAndResize(x, y, w, h int) {
	vs := [4]int{x, y, w, h}
	if r.lastPos == vs {
		return
	}
	r.lastPos = vs

	x -= int(r.bw)
	y -= int(r.bw)
	w += int(2 * r.bw)
	h += int(2 * r.bw)

	rects := []xproto.Rectangle{
		{X: 0, Y: 0, Width: uint16(w), Height: r.bw},
		{X: int16(uint16(w) - r.bw), Y: 0, Width: r.bw, Height: uint16(h)},
		{X: 0, Y: int16(uint16(h) - r.bw), Width: uint16(w), Height: r.bw},
		{X: 0, Y: 0, Width: r.bw, Height: uint16(h)},
	}
	shape.Rectangles(r.wm.X.Conn(), shape.SoSet, shape.SkBounding, 0, r.win.Id, 0, 0, []xproto.Rectangle{})
	shape.Rectangles(r.wm.X.Conn(), shape.SoSet, shape.SkClip, 0, r.win.Id, 0, 0, []xproto.Rectangle{})
	r.win.MoveResize(x, y, w, h)
	shape.Rectangles(r.wm.X.Conn(), shape.SoSet, shape.SkBounding, 0, r.win.Id, 0, 0, rects)
	shape.Rectangles(r.wm.X.Conn(), shape.SoSet, shape.SkClip, 0, r.win.Id, 0, 0, rects)
}

func (win *Window) FillSelect() {
	r, err := win.wm.NewRectangle(5, 0x00FF00)
	if err != nil {
		log.Println("couldn't create rectangle:", err)
		return
	}
	r.Show()
	ok, err := mousebind.GrabPointer(win.wm.X, r.Id(), 0, 0)
	if err != nil {
		log.Println("err in grab:", err)
		return
	}
	if !ok {
		log.Println("couldn't grab pointer")
		return
	}

	win.PushLayout()

	cbClick := func(ev xevent.ButtonPressEvent) {
		win.Layout.X = int(ev.RootX)
		win.Layout.Y = int(ev.RootY)
		win.Layout.Width = 1
		win.Layout.Height = 1
		win.fill()
	}

	if err := keybind.GrabKeyboard(win.wm.X, r.Id()); err != nil {
		log.Println("couldn't grab keyboard:", err)
	}

	cleanup := func() {
		xevent.Detach(win.wm.X, r.Id())
		mousebind.DetachPress(win.wm.X, r.Id())
		mousebind.UngrabPointer(win.wm.X)
		keybind.UngrabKeyboard(win.wm.X)
		r.Destroy()
	}
	fn := mousebind.ButtonPressFun(func(xu *xgbutil.XUtil, event xevent.ButtonPressEvent) {
		cleanup()
		cbClick(event)
	})
	if err := fn.Connect(win.wm.X, r.Id(), "1", false, false); err != nil {
		log.Println("err in connect:", err)
		cleanup()
		return
	}
	fn2 := keybind.KeyPressFun(func(xu *xgbutil.XUtil, ev xevent.KeyPressEvent) {
		cleanup()
	})
	if err := fn2.Connect(win.wm.X, r.win.Id, "Escape", true); err != nil {
		log.Println("couldn't register keybind:", err)
	}

	wins := win.wm.VisibleWindows()
	calculateFill := func(x, y int) {
		l := win.Layout
		win.Layout.X = x
		win.Layout.Y = y
		win.Layout.Width = 1
		win.Layout.Height = 1
		r.MoveAndResize(win.calculateFill(wins))
		win.Layout = l
	}

	t := time.Now()
	cbMove := func(xu *xgbutil.XUtil, ev xevent.MotionNotifyEvent) {
		if time.Since(t) < 10*time.Millisecond {
			return
		}
		t = time.Now()
		calculateFill(int(ev.RootX), int(ev.RootY))
	}
	pos := win.wm.PointerPos()
	calculateFill(pos.X, pos.Y)
	xevent.MotionNotifyFun(cbMove).Connect(win.wm.X, r.Id())
}

func (win *Window) Fill() {
	win.PushLayout()
	win.fill()
}

func (win *Window) calculateFill(wins []*Window) (x, y, w, h int) {
	l := win.Layout
	defer func() { win.Layout = l }()

	left1, _, right1, _ := win.collisions(wins)
	win.Layout.X = left1
	win.Layout.Width = right1 - left1
	_, top1, _, bottom1 := win.collisions(wins)
	ww := float64(right1 - left1)
	wh := float64(bottom1 - top1)
	square1 := math.Min(ww, wh) / math.Max(ww, wh)

	win.Layout = l
	_, top2, _, bottom2 := win.collisions(wins)
	win.Layout.Y = top2
	win.Layout.Height = bottom2 - top2
	left2, _, right2, _ := win.collisions(wins)
	ww = float64(right2 - left2)
	wh = float64(bottom2 - top2)
	square2 := math.Min(ww, wh) / math.Max(ww, wh)

	if square1 > square2 {
		return left1, top1, right1 - left1, bottom1 - top1
	}
	return left2, top2, right2 - left2, bottom2 - top2
}

func (win *Window) fill() {
	wins := win.wm.VisibleWindows()
	x, y, w, h := win.calculateFill(wins)
	win.Layout.X = x
	win.Layout.Y = y
	win.Layout.Width = w
	win.Layout.Height = h
	win.moveAndResizeNoReset()
}

func (win *Window) FillUp() {
	_, top, _, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	oy := win.Layout.Y
	win.Layout.Y = top
	win.Layout.Height += oy - win.Layout.Y
	win.moveAndResizeNoReset()
}

func (win *Window) FillDown() {
	_, _, _, bottom := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	win.Layout.Height = bottom - win.Layout.Y
	win.moveAndResizeNoReset()
}

func (win *Window) FillLeft() {
	left, _, _, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	ox := win.Layout.X
	win.Layout.X = left
	win.Layout.Width += ox - win.Layout.X
	win.moveAndResizeNoReset()
}

func (win *Window) FillRight() {
	_, _, right, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	win.Layout.Width = right - win.Layout.X
	win.moveAndResizeNoReset()
}

func (win *Window) PushUp() {
	_, top, _, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	oldY := win.Layout.Y
	win.Layout.Y = top
	win.wm.WarpPointerRel(0, win.Layout.Y-oldY)
	win.moveNoReset()
}

func (win *Window) PushDown() {
	_, _, _, bottom := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	oldY := win.Layout.Y
	win.Layout.Y = bottom - win.Layout.Height
	win.wm.WarpPointerRel(0, win.Layout.Y-oldY)
	win.moveNoReset()
}

func (win *Window) PushLeft() {
	left, _, _, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	oldX := win.Layout.X
	win.Layout.X = left
	win.wm.WarpPointerRel(win.Layout.X-oldX, 0)
	win.moveNoReset()
}

func (win *Window) PushRight() {
	_, _, right, _ := win.collisions(win.wm.MappedWindows())

	win.PushLayout()
	oldX := win.Layout.X
	win.Layout.X = right - win.Layout.Width
	win.wm.WarpPointerRel(win.Layout.X-oldX, 0)
	win.moveNoReset()
}

func (win *Window) Maximize(state MaximizedState) {
	// TODO what about min/max size and increments?

	win.PushLayout()

	sc := win.Screen().subtractGap(win.wm.Config.Gap)
	if (state & MaximizedH) > 0 {
		win.Layout.X = sc.X
		win.Layout.Width = sc.Width - 2*win.wm.Config.BorderWidth
	}
	if (state & MaximizedV) > 0 {
		win.Layout.Y = sc.Y
		win.Layout.Height = sc.Height - 2*win.wm.Config.BorderWidth
	}
	win.moveAndResizeNoReset()
	win.Layout.State |= state
	win.updateWmState()
}

func (win *Window) ContainsPointer() bool {
	return win.Layout.Contains(win.wm.PointerPos())
}

func (win *Window) CenterPointer() {
	xproto.WarpPointer(win.wm.X.Conn(), xproto.WindowNone, win.Id, 0, 0, 0, 0,
		int16(win.Layout.Width/2-win.wm.Config.BorderWidth), int16(win.Layout.Height/2-win.wm.Config.BorderWidth))
}

// move moves the window based on its current Geom. It also resets the
// window's maximized state.
func (win *Window) move() {
	win.Window.Move(win.Layout.X, win.Layout.Y)
	win.Layout.State &= ^MaximizedFull
	win.updateWmState()
}

// resize resizes the window based on its current Geom. It also resets
// the window's maximized state.
func (win *Window) resize() {
	win.Window.Resize(win.Layout.Width, win.Layout.Height)
	win.Layout.State &= ^MaximizedFull
	win.updateWmState()
}

// moveAndResize moves and resizes the window based on its current
// Geom. It also resets the window's maximized state.
func (win *Window) moveAndResize() {
	win.Window.MoveResize(win.Layout.X, win.Layout.Y, win.Layout.Width, win.Layout.Height)
	win.Layout.State &= ^MaximizedFull
	win.updateWmState()
}

// move moves the window based on its current Geom.
func (win *Window) moveNoReset() {
	win.Window.Move(win.Layout.X, win.Layout.Y)
}

// moveAndResize moves and resizes the window based on its current
// Geom.
func (win *Window) moveAndResizeNoReset() {
	win.Window.MoveResize(win.Layout.X, win.Layout.Y, win.Layout.Width, win.Layout.Height)
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
	win.SetBorderColor(win.wm.Color(win.wm.Config.Colors["activeborder"]))
	win.Focus()
	if curwin := win.wm.CurWindow; curwin != nil {
		curwin.SetBorderColor(win.wm.Color(win.wm.Config.Colors["inactiveborder"]))
	}
	win.wm.CurWindow = win
}

func (win *Window) Focus() {
	win.Window.Focus()
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
	win.overlay = nil
	delete(win.wm.Windows, win.Id)
}

func (win *Window) UnmapNotify(xu *xgbutil.XUtil, ev xevent.UnmapNotifyEvent) {
	LogWindowEvent(win, "Unmapping")
	win.Mapped = false
	win.State = icccm.StateWithdrawn
	icccm.WmStateSet(win.wm.X, win.Id, &icccm.WmState{State: uint(win.State)})
}

func (win *Window) ShowOverlay() {
	if win.overlay == nil {
		return
	}
	win.overlay.Map()
}

func (win *Window) HideOverlay() {
	if win.overlay == nil {
		return
	}
	win.overlay.Unmap()
}

func (win *Window) WriteToOverlay(s string) {
	if win.overlay == nil {
		return
	}
	draw.Fill(win.overlay, win.overlay.Geom.Width(), win.overlay.Geom.Height(), 0xFFFFFF)
	w, h := draw.Text(win.overlay, s, win.wm.font, 0, 0xFFFFFF, 0, 0)
	win.overlay.Resize(w, h)
}

func (win *Window) Init() {
	// TODO do something if the state is iconified
	LogWindowEvent(win, "Initializing")
	win.SetBorderWidth(win.wm.Config.BorderWidth)
	win.SetBorderColor(win.wm.Color(win.wm.Config.Colors["inactiveborder"]))

	attr, err := xproto.GetGeometry(win.wm.X.Conn(), xproto.Drawable(win.Id)).Reply()
	if err != nil {
		should(err)
	} else {
		win.Layout.X = int(attr.X)
		win.Layout.Y = int(attr.Y)
		win.Layout.Width = int(attr.Width)
		win.Layout.Height = int(attr.Height)
	}
	if win.Layout.Y > win.Screen().Height {
		win.Layout.Y = win.Screen().Height - win.Layout.Height
	}

	states, err := ewmh.WmStateGet(win.wm.X, win.Id)
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

	w, err := xwindow.Create(win.wm.X, win.wm.Root.Id)
	should(err)
	if err == nil {
		// For some reason we need to create and then reparent.
		// Directly specifying a window as the parent might fail with
		// BadMatch, for example with mplayer.
		err := xproto.ReparentWindowChecked(win.wm.X.Conn(), w.Id, win.Id, 0, 0).Check()
		should(err)
		if err == nil {
			win.overlay = win.wm.NewWindow(w.Id)
		}
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
			EventX:   int16(data[0]) - int16(win.Layout.X),
			EventY:   int16(data[1]) - int16(win.Layout.Y),
			// FIXME what about Event, Child, State and SameScreen?
		}

		switch data[2] {
		case ewmh.Move:
			mousebind.DragBegin(win.wm.X, xevent.ButtonPressEvent{ButtonPressEvent: ev}, win.Id, win.Id,
				win.MoveBegin, win.MoveStep, win.MoveEnd)
			return
		case ewmh.MoveKeyboard, ewmh.SizeKeyboard:
			return
		case ewmh.Cancel:
			mousebind.DragEnd(win.wm.X, xevent.ButtonReleaseEvent{ButtonReleaseEvent: (*xproto.ButtonReleaseEvent)(ev)})
		default:
			mousebind.DragBegin(win.wm.X, xevent.ButtonPressEvent{ButtonPressEvent: ev}, win.Id, win.Id,
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
	case "_NET_WM_STATE_MAXIMIZED_VERT":
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
	case "_NET_WM_STATE_MAXIMIZED_VERT":
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
	for _, ow := range win.wm.MappedWindows() {
		windows[ow.Layer] = append(windows[ow.Layer], ow)
	}

	var update []*Window
	for layer := LayerDesktop; layer <= LayerAbove; layer++ {
		update = append(update, windows[layer]...)
	}
	win.wm.Restack(update)
}

func (win *Window) SendStructureNotify() {
	LogWindowEvent(win, "Sending StructureNotify")
	log.Printf("\tX: %d Y: %d W: %d H: %d", win.Layout.X, win.Layout.Y, win.Layout.Width, win.Layout.Height)
	ev := xproto.ConfigureNotifyEvent{
		Event:            win.Id,
		Window:           win.Id,
		AboveSibling:     xevent.NoWindow,
		X:                int16(win.Layout.X),
		Y:                int16(win.Layout.Y),
		Width:            uint16(win.Layout.Width),
		Height:           uint16(win.Layout.Height),
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
	repl, err := xprop.GetProperty(win.wm.X, win.Id, "WM_CLASS")
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

func (win *Window) Center() Point {
	return Point{win.Layout.X + win.Layout.Width/2,
		win.Layout.Y + win.Layout.Height/2}
}

func (win *Window) Screen() Geometry {
	screens := win.wm.Screens()
	return screenForPoint(screens, win.Center())
}

func (win *Window) updateWmState() {
	var atoms []string
	if (win.Layout.State & MaximizedH) > 0 {
		atoms = append(atoms, "_NET_WM_STATE_MAXIMIZED_HORZ")
	}
	if (win.Layout.State & MaximizedV) > 0 {
		atoms = append(atoms, "_NET_WM_STATE_MAXIMIZED_VERT")
	}
	if win.Layout.State == Fullscreen {
		atoms = append(atoms, "_NET_WM_STATE_FULLSCREEN")
	}
	if win.Layer == LayerAbove {
		atoms = append(atoms, "_NET_WM_STATE_ABOVE")
	}
	if win.Layer == LayerBelow {
		atoms = append(atoms, "_NET_WM_STATE_BELOW")
	}
	// TODO other hints
	ewmh.WmStateSet(win.wm.X, win.Id, atoms)
}

func (wm *WM) CycleScreens() {
	// TODO currently, this only supports screens that are placed next
	// to each other, not above/below. They also need to have the same
	// height.
	screens := wm.Screens()
	if len(screens) != 2 {
		return
	}

	width := 0
	for _, sc := range screens {
		width += sc.Width
	}

	for _, win := range wm.Windows {
		win.Layout.X = (win.Layout.X + win.Screen().Width) % width
		// FIXME this clears the maximized state
		win.move()
	}
}

type WM struct {
	X         *xgbutil.XUtil
	Cursors   map[string]xproto.Cursor
	Root      *Window
	Config    *config.Config
	Windows   map[xproto.Window]*Window
	CurWindow *Window
	chFn      chan func()
	font      xproto.Font
	colors    map[string]int
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
	// FIXME why do we split work across MapRequest and Init? should
	// these be collapsed into a single function? does anyone else
	// call Init?
	//
	// Yes, we call Init when the WM first starts
	win.Init()

	normalHints, err := icccm.WmNormalHintsGet(win.wm.X, win.Id)
	if err != nil || (normalHints.Flags&(icccm.SizeHintPPosition|icccm.SizeHintUSPosition) == 0) {
		if win.Layout.State == 0 && win.Layout.State != Fullscreen {
			ptr := win.wm.PointerPos()
			win.Layout.X = ptr.X - win.Layout.Width/2
			win.Layout.Y = ptr.Y - win.Layout.Height/2
		}
	}

	win.moveNoReset()
	win.Map()
	win.Raise()
	// TODO probably should
	// a) store the border width in every client
	// b) use that for all calculations involving the border width
	win.CenterPointer()
	if (hints.Flags & icccm.HintState) == 0 {
		hints.InitialState = icccm.StateNormal
	}
	icccm.WmStateSet(wm.X, win.Id, &icccm.WmState{State: hints.InitialState, Icon: 0})
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
		win.Layout.Width = int(ev.Width)
	}
	if (m & xproto.ConfigWindowHeight) > 0 {
		win.Layout.Height = int(ev.Height)
	}
	if (m & xproto.ConfigWindowX) > 0 {
		win.Layout.X = int(ev.X)
	}
	if (m & xproto.ConfigWindowY) > 0 {
		win.Layout.Y = int(ev.Y)
	}

	if win.Layout.X < 0 {
		win.Layout.X = 0
	}

	if win.Layout.Y < 0 {
		win.Layout.Y = 0
	}

	// TODO stack order, border width, sibling

	win.Configure(int(ev.ValueMask) & ^(xproto.ConfigWindowSibling|xproto.ConfigWindowStackMode),
		win.Layout.X,
		win.Layout.Y,
		win.Layout.Width,
		win.Layout.Height,
		0,
		0,
	)

	win.SendStructureNotify()
}

func (wm *WM) NewWindow(c xproto.Window) *Window {
	if win, ok := wm.Windows[c]; ok {
		return win
	}

	win := &Window{wm: wm, Window: xwindow.New(wm.X, c), gcs: make(draw.GCs)}
	LogWindowEvent(win, "Managing window")
	wm.Windows[c] = win

	attr := win.Attributes()
	if attr != nil {
		switch attr.MapState {
		case xproto.MapStateUnmapped:
			// TODO how do we differentiate between withdrawn and iconified?
			win.State = icccm.StateWithdrawn
		case xproto.MapStateUnviewable, xproto.MapStateViewable:
			win.Mapped = true
			win.State = icccm.StateNormal
		}
	}

	should(win.Listen(xproto.EventMaskEnterWindow,
		xproto.EventMaskStructureNotify))

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
		if err != nil {
			continue
		}
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

func (wm *WM) PointerPos() Point {
	ptr, err := xproto.QueryPointer(wm.X.Conn(), wm.Root.Id).Reply()
	if err != nil {
		log.Println("Could not query pointer position:", err)
		return Point{}
	}
	return Point{int(ptr.RootX), int(ptr.RootY)}
}

func (wm *WM) CurrentScreen() Geometry {
	screens := wm.Screens()
	return screenForPoint(screens, wm.PointerPos())
}

func (wm *WM) debug() {
	log.Println("START DEBUG")
	log.Printf("- Managing %d windows", len(wm.Windows))
	log.Println("END DEBUG")
}

func (wm *WM) Restart() {
	log.Println("Restarting gwm")
	if err := syscall.Exec(os.Args[0], os.Args, os.Environ()); err != nil {
		log.Println("exec failed:", err)
	}
}

func (wm *WM) WarpPointer(d Point) {
	s := wm.PointerPos()
	dx := d.X - s.X
	dy := d.Y - s.Y
	xproto.WarpPointer(wm.X.Conn(), xproto.WindowNone, xproto.WindowNone, 0, 0, 0, 0, int16(dx), int16(dy))
}

func (wm *WM) WarpPointerRel(dx, dy int) {
	xproto.WarpPointer(wm.X.Conn(), xproto.WindowNone, xproto.WindowNone, 0, 0, 0, 0, int16(dx), int16(dy))
}

func (wm *WM) windowSearchMenu() {
	wins := wm.MappedWindows() // FIXME hidden windows
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

	m, err := wm.newMenu("window", entries, filter)
	if err != nil {
		log.Println("Could not display menu:", err)
		return
	}
	m.Show()
	go func() {
		if ret, ok := m.Wait(); ok && !ret.Synthetic() {
			wm.chFn <- func() {
				ret.Payload.(*Window).Activate()
			}
		}
	}()
}

func (wm *WM) newMenu(title string, entries []menu.Entry, filter menu.FilterFunc) (*menu.Menu, error) {
	p := wm.PointerPos()
	sc := wm.CurrentScreen().subtractGap(wm.Config.Gap)
	m, err := menu.New(wm.X, title, menu.Config{
		X:           p.X,
		Y:           p.Y,
		MinY:        wm.Config.Gap.Top,
		MaxHeight:   sc.Height,
		BorderWidth: wm.Config.BorderWidth,
		BorderColor: wm.Color(wm.Config.Colors["activeborder"]),
		Font:        wm.font,
		FilterFn:    filter,
	})
	if err != nil {
		return nil, err
	}
	m.SetEntries(entries)
	return m, nil
}

func (wm *WM) acquireOwnership(replace bool) error {
	existingWM := false
	var oldWin *xwindow.Window

	selAtom, err := xprop.Atm(wm.X, fmt.Sprintf("WM_S%d", wm.X.Conn().DefaultScreen))
	must(err)

	reply, err := xproto.GetSelectionOwner(wm.X.Conn(), selAtom).Reply()
	must(err)
	if reply.Owner != xproto.WindowNone {
		if !replace {
			log.Println("A WM is already running")
			return errors.New("a WM is already running")
		}
		log.Println("A WM is already running, replacing it...")
		existingWM = true
		oldWin = xwindow.New(wm.X, reply.Owner)
		err = oldWin.Listen(xproto.EventMaskStructureNotify)
		must(err)
	}
	err = xproto.SetSelectionOwnerChecked(wm.X.Conn(), wm.X.Dummy(), selAtom, 0).Check()
	must(err)

	reply, err = xproto.GetSelectionOwner(wm.X.Conn(), selAtom).Reply()
	must(err)
	if reply.Owner != wm.X.Dummy() {
		log.Println("Could not get ownership") // FIXME better error
		return errors.New("Could not get ownership")
	}

	if existingWM {
		timeout := time.After(3 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
	killLoop:
		for {
			select {
			case <-timeout:
				log.Println("Killing misbehaving WM")
				oldWin.Kill()
				break killLoop
			case <-ticker.C:
				ev, err := wm.X.Conn().PollForEvent()
				if err != nil {
					continue
				}
				if destNotify, ok := ev.(xproto.DestroyNotifyEvent); ok {
					if destNotify.Window == oldWin.Id {
						break killLoop
					}
				}

			}
		}
		ticker.Stop()
	}

	wm.announce()

	xevent.SelectionClearFun(func(xu *xgbutil.XUtil, ev xevent.SelectionClearEvent) {
		log.Println("A different WM is replacing us")
		xevent.Quit(xu)
	}).Connect(wm.X, wm.X.Dummy())

	return nil
}

func (wm *WM) announce() {
	typAtom, err := xprop.Atm(wm.X, "MANAGER")
	must(err)
	manSelAtom, err := xprop.Atm(wm.X, fmt.Sprintf("WM_S%d", wm.X.Conn().DefaultScreen))
	must(err)
	cm, err := xevent.NewClientMessage(32, wm.X.RootWin(), typAtom,
		int(wm.X.TimeGet()), int(manSelAtom), int(wm.X.Dummy()))
	must(err)
	xproto.SendEvent(wm.X.Conn(), false, wm.X.RootWin(), xproto.EventMaskStructureNotify, string(cm.Bytes()))
}

func (wm *WM) Color(name string) int {
	if color, ok := wm.colors[name]; ok {
		return color
	}

	if name[0] == '#' {
		i, _ := strconv.ParseInt(name[1:], 16, 32)
		wm.colors[name] = int(i)
		return int(i)
	}

	reply, err := xproto.LookupColor(wm.X.Conn(), wm.X.Screen().DefaultColormap, uint16(len(name)), name).Reply()
	should(err)
	color := int(reply.ExactRed/256)<<16 | int(reply.ExactGreen/256)<<8 | int(reply.ExactBlue/256)
	if err != nil {
		color = 0
	}
	wm.colors[name] = color
	return color
}

func (wm *WM) Init(xu *xgbutil.XUtil) {
	var err error
	wm.X = xu
	// TODO make replacing the WM optional
	if err := wm.acquireOwnership(true); err != nil {
		return
	}
	wm.LoadCursors(map[string]uint16{
		"fleur":               xcursor.Fleur,
		"normal":              xcursor.LeftPtr,
		"top_left_corner":     xcursor.TopLeftCorner,
		"top_right_corner":    xcursor.TopRightCorner,
		"bottom_left_corner":  xcursor.BottomLeftCorner,
		"bottom_right_corner": xcursor.BottomRightCorner,
	})

	wm.font, err = xproto.NewFontId(xu.Conn())
	must(err)

	name := "-*-terminus-*-r-*-*-20-*-*-*-*-*-iso10646-*"
	err = xproto.OpenFontChecked(xu.Conn(), wm.font, uint16(len(name)), name).Check()
	if err != nil {
		log.Fatalln("couldn't load font:", err)
	}

	mousebind.Initialize(wm.X)
	keybind.Initialize(wm.X)
	if err := shape.Init(wm.X.Conn()); err != nil {
		log.Fatal("couldn't initialize X Shape Extension")
	}

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
		// "WM_TAKE_FOCUS",
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

	must(ewmh.SupportingWmCheckSet(wm.X, wm.Root.Id, wm.X.Dummy()))
	must(ewmh.SupportingWmCheckSet(wm.X, wm.X.Dummy(), wm.X.Dummy()))
	must(ewmh.WmNameSet(wm.X, wm.X.Dummy(), "gwm"))

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
		colors:  make(map[string]int),
	}
	xu, err := xgbutil.NewConn()
	must(err)

	/*
		laddr, err := net.ResolveUnixAddr("unix", "/tmp/gwm-1")
		if err != nil {
			panic(err)
		}

			go func() {
				srv, err := net.ListenUnix("unix", laddr)
				if err != nil {
					panic(err)
				}
				for {
					conn, err := srv.Accept()
					if err != nil {
						panic(err)
					}

					if err := p9p.ServeConn(context.Background(), conn, p9p.Dispatch(newSession(wm))); err != nil {
						log.Println(err)
					}
				}
			}()
	*/

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
	go cmd.Process.Wait()
	return nil
}

func winmovefunc(xf, yf int) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		win := wm.CurWindow
		dx := xf * wm.Config.MoveAmount
		dy := yf * wm.Config.MoveAmount
		win.Move(win.Layout.X+dx, win.Layout.Y+dy)
		wm.WarpPointerRel(dx, dy)
		// TODO apply snapping
	}
}

func winmaximizefunc(state MaximizedState) func(*WM) {
	return func(wm *WM) {
		if wm.CurWindow == nil {
			return
		}
		wm.CurWindow.Maximize(state)
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

type Layout struct {
	Geometry
	State MaximizedState
}

var commands = map[string]func(wm *WM){
	"lower":        winfunc((*Window).Lower),
	"raise":        winfunc((*Window).Raise),
	"fill":         winfunc((*Window).Fill),
	"fillup":       winfunc((*Window).FillUp),
	"filldown":     winfunc((*Window).FillDown),
	"fillleft":     winfunc((*Window).FillLeft),
	"fillright":    winfunc((*Window).FillRight),
	"fillsel":      winfunc((*Window).FillSelect),
	"pushup":       winfunc((*Window).PushUp),
	"pushdown":     winfunc((*Window).PushDown),
	"pushleft":     winfunc((*Window).PushLeft),
	"pushright":    winfunc((*Window).PushRight),
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
	"poplayout":    winfunc((*Window).PopLayout),
	"cycle":        (*WM).CycleScreens,

	"debug":   (*WM).debug,
	"restart": (*WM).Restart,

	"terminal": func(wm *WM) {
		if cmd, ok := wm.Config.Commands["term"]; ok {
			execute(cmd)
		}
	},

	"exec": func(wm *WM) {
		entries := executables()
		m, err := wm.newMenu("exec", entries, menu.FilterPrefix)
		if err != nil {
			log.Println("Could not display menu:", err)
			return
		}
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
// TODO remove wm_state when withdrawing
// TODO unset _NET_DESKTOP_NAMES
// TODO set allowed actions
