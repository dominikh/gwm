// +build debug

package quadtree

import (
	"image"
	"image/color"
	"image/draw"
)

func (n *Node) Render() image.Image {
	r := image.Rectangle{
		Min: image.Pt(0, 0),
		Max: image.Pt(n.Width, n.Height),
	}
	img := image.NewGray(r)
	draw.Draw(img, r, &image.Uniform{color.Gray{Y: 255}}, image.ZP, draw.Src)
	n.render(img)
	return img
}

func (n *Node) render(img draw.Image) {
	if !n.isSplit {
		if n.Value > 0 {
			r := image.Rectangle{
				Min: image.Pt(n.X, n.Y),
				Max: image.Pt(n.X+n.Width, n.Y+n.Height),
			}
			draw.Draw(img, r, &image.Uniform{color.Gray{uint8(n.Value)}}, image.ZP, draw.Src)
		}
	}
	r1 := image.Rectangle{
		Min: image.Pt(n.X, n.Y),
		Max: image.Pt(n.X+n.Width, n.Y+1),
	}
	r2 := image.Rectangle{
		Min: image.Pt(n.X, n.Y),
		Max: image.Pt(n.X+1, n.Y+n.Height),
	}
	draw.Draw(img, r1, &image.Uniform{color.Gray{uint8(0)}}, image.ZP, draw.Src)
	draw.Draw(img, r2, &image.Uniform{color.Gray{uint8(0)}}, image.ZP, draw.Src)
	if !n.isSplit {
		return
	}
	for i := range n.children {
		n.children[i].render(img)
	}
}
