package quadtree

type Region struct {
	X      int
	Y      int
	Width  int
	Height int
}

func round(n int) int {
	if n&(n-1) == 0 {
		return n
	}
	v := uint64(n)
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v |= v >> 32
	v++
	return int(v)
}

type Node struct {
	X     int
	Y     int
	Size  int
	Value int

	children []Node
	isSplit  bool
}

func New(size int) *Node {
	return &Node{
		Size: round(size),
	}
}

func (n *Node) overlaps(other Region) (ret bool) {
	x1, y1 := n.X, n.Y
	x2, y2 := n.X+n.Size, n.Y+n.Size

	ox1, oy1 := other.X, other.Y
	ox2, oy2 := other.X+other.Width, other.Y+other.Height

	return x1 < ox2 && x2 > ox1 && y1 < oy2 && y2 > oy1
}

func (n *Node) Set(r Region, value int) {
	if !n.isSplit {
		if n.X >= r.X && n.Y >= r.Y &&
			n.X+n.Size <= r.X+r.Width && n.Y+n.Size <= r.Y+r.Height {
			n.Value = value
			return
		}
		n.split()
	}
	for i := range n.children {
		if n.children[i].overlaps(r) {
			n.children[i].Set(r, value)
		}
	}
}

func (n *Node) quadrant(x, y int) *Node {
	if !n.isSplit {
		return n
	}
	quadrant := 0
	if x >= n.X+n.Size/2 {
		quadrant++
	}
	if y >= n.Y+n.Size/2 {
		quadrant += 2
	}
	return n.children[quadrant].quadrant(x, y)
}

func (n *Node) Get(x, y int) int {
	return n.quadrant(x, y).Value
}

func (n *Node) split() {
	size := n.Size / 2
	n.children = make([]Node, 4)
	n.children[0] = Node{
		X:     n.X,
		Y:     n.Y,
		Size:  size,
		Value: n.Value,
	}
	n.children[1] = Node{
		X:     n.X + size,
		Y:     n.Y,
		Size:  size,
		Value: n.Value,
	}
	n.children[2] = Node{
		X:     n.X,
		Y:     n.Y + size,
		Size:  size,
		Value: n.Value,
	}
	n.children[3] = Node{
		X:     n.X + size,
		Y:     n.Y + size,
		Size:  size,
		Value: n.Value,
	}
	n.isSplit = true
}
