package quadtree

type Region struct {
	X      int
	Y      int
	Width  int
	Height int
}

func (r Region) Overlaps(other Region) (ret bool) {
	x1, y1 := r.X, r.Y
	x2, y2 := r.X+r.Width, r.Y+r.Height

	ox1, oy1 := other.X, other.Y
	ox2, oy2 := other.X+other.Width, other.Y+other.Height

	return x1 < ox2 && x2 > ox1 && y1 < oy2 && y2 > oy1
}

type Node struct {
	Region
	Value int

	children []Node
	isSplit  bool
}

func New(width, height int) *Node {
	return &Node{
		Region: Region{
			Width:  width,
			Height: height,
		},
	}
}

func (n *Node) Set(r Region, value int) {
	if !n.isSplit {
		if n.X >= r.X && n.Y >= r.Y &&
			n.X+n.Width <= r.X+r.Width && n.Y+n.Height <= r.Y+r.Height {
			n.Value = value
			return
		}
		n.split()
	}
	for i := range n.children {
		if n.children[i].Overlaps(r) {
			n.children[i].Set(r, value)
		}
	}
}

func (n *Node) quadrant(x, y int) *Node {
	if !n.isSplit {
		return n
	}
	quadrant := 0
	if x > n.Width/2 {
		quadrant++
	}
	if y > n.Height/2 {
		quadrant += 2
	}
	return n.children[quadrant].quadrant(x, y)
}

func (n *Node) Get(x, y int) int {
	return n.quadrant(x, y).Value
}

func (n *Node) split() {
	width, height := n.Width/2, n.Height/2
	n.children = make([]Node, 4)
	n.children[0] = Node{Region: Region{
		X:      n.X,
		Y:      n.Y,
		Width:  width,
		Height: height,
	}, Value: n.Value}
	n.children[1] = Node{Region: Region{
		X:      n.X + width,
		Y:      n.Y,
		Width:  width,
		Height: height,
	}, Value: n.Value}
	n.children[2] = Node{Region: Region{
		X:      n.X,
		Y:      n.Y + height,
		Width:  width,
		Height: height,
	}, Value: n.Value}
	n.children[3] = Node{Region: Region{
		X:      n.X + width,
		Y:      n.Y + height,
		Width:  width,
		Height: height,
	}, Value: n.Value}
	n.isSplit = true
}
