package draw

// TODO modify menu package to use this one
// FIXME get rid of all panics

import (
	"unicode/utf16"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
)

type gcSpec struct {
	mask uint32
	fg   int
	bg   int
	font xproto.Font
	win  xproto.Window
}

type GCs map[gcSpec]xproto.Gcontext

type Drawable interface {
	GCs() GCs
	Win() xproto.Window
	X() *xgbutil.XUtil
}

func Fill(d Drawable, w int, h int, fg int) {
	spec := gcSpec{
		mask: uint32(xproto.GcForeground),
		fg:   fg,
		win:  d.Win(),
	}

	gcs := d.GCs()
	gc, ok := gcs[spec]
	if !ok {
		gc, _ = xproto.NewGcontextId(d.X().Conn())
		xproto.CreateGC(d.X().Conn(), gc, xproto.Drawable(d.Win()), spec.mask,
			[]uint32{uint32(fg)})
		gcs[spec] = gc
	}

	xproto.PolyFillRectangle(d.X().Conn(), xproto.Drawable(d.Win()), gc,
		[]xproto.Rectangle{{0, 0, uint16(w), uint16(h)}})
}

func Text(d Drawable, text string, font xproto.Font, fg int, bg int,
	x int, y int) (w int, h int) {

	spec := gcSpec{
		mask: uint32(xproto.GcForeground | xproto.GcBackground | xproto.GcFont),
		fg:   fg,
		bg:   bg,
		font: font,
		win:  d.Win(),
	}

	gcs := d.GCs()
	gc, ok := gcs[spec]
	if !ok {
		gc, _ = xproto.NewGcontextId(d.X().Conn())
		xproto.CreateGC(d.X().Conn(), gc, xproto.Drawable(d.Win()), spec.mask,
			[]uint32{uint32(fg), uint32(bg), uint32(font)})
		gcs[spec] = gc
	}

	r := []rune(text)
	chars, n := toChar2b(r)

	ex, err := xproto.QueryTextExtents(d.X().Conn(), xproto.Fontable(font), chars, 0).Reply()
	if err != nil {
		panic(err)
	}

	y = int(int16(y) + ex.FontAscent)

	err = xproto.ImageText16Checked(d.X().Conn(), byte(n), xproto.Drawable(d.Win()), gc,
		int16(x), int16(y), chars).Check()
	if err != nil {
		panic(err)
	}

	return int(ex.OverallRight), int(ex.FontAscent) + int(ex.FontDescent)
}

func toChar2b(runes []rune) ([]xproto.Char2b, int) {
	ucs2 := utf16.Encode(runes)
	var chars []xproto.Char2b
	for _, r := range ucs2 {
		chars = append(chars, xproto.Char2b{byte(r >> 8), byte(r)})
	}
	return chars, len(runes)
}
