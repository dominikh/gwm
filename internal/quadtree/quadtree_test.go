package quadtree

import "testing"

func TestRound(t *testing.T) {
	var tests = []struct {
		in, out int
	}{
		{2, 2},
		{4, 4},
		{7, 8},
		{1920, 2048},
	}
	for _, tt := range tests {
		if ret := round(tt.in); ret != tt.out {
			t.Errorf("round(%d) = %d, want %d", tt.in, ret, tt.out)
		}
	}
}

func TestTree(t *testing.T) {
	q := New(1920, 1080)
	q.Set(Region{9, 9, 9, 9}, 70)
	q.Set(Region{360, 360, 360, 360}, 123)
	q.Set(Region{300, 300, 360, 360}, 50)

	var tests = []struct {
		x, y int
		out  int
	}{
		{9, 9, 70},
		{13, 10, 70},
		{400, 400, 50},
		{700, 700, 123},
	}

	for _, tt := range tests {
		if ret := q.Get(tt.x, tt.y); ret != tt.out {
			t.Errorf("q.Get(%d, %d) = %d, want %d", tt.x, tt.y, ret, tt.out)
		}
	}
}
