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

func BenchmarkConstruction(b *testing.B) {
	for i := 0; i < b.N; i++ {
		q := New(3840, 2160)
		q.Set(Region{0, 0, 3488, 1638}, int(48234500))
		q.Set(Region{1413, 952, 712, 905}, int(37748740))
		q.Set(Region{1600, 751, 2088, 1301}, int(20971521))
		q.Set(Region{0, 0, 1944, 1004}, int(31457286))
		q.Set(Region{343, 338, 804, 484}, int(58720262))
		q.Set(Region{2448, 1213, 1284, 544}, int(50331654))
		q.Set(Region{0, 0, 1204, 584}, int(33554438))
		q.Set(Region{2584, 11, 1254, 684}, int(44040198))
		q.Set(Region{2372, 1188, 1374, 784}, int(29360134))
		q.Set(Region{2364, 14, 1474, 444}, int(39845894))
		q.Set(Region{290, 1497, 804, 484}, int(56623110))
		q.Set(Region{2652, 997, 1774, 644}, int(35651590))
		q.Set(Region{0, 1614, 1444, 544}, int(16777222))
		q.Set(Region{1362, 290, 1653, 1260}, int(20971577))
		q.Set(Region{1228, 352, 1840, 1500}, int(12582932))
		q.Set(Region{2102, 709, 1424, 1024}, int(67108870))
	}
}

func BenchmarkRetrieve(b *testing.B) {
	q := New(3840, 2160)
	q.Set(Region{0, 0, 3488, 1638}, int(48234500))
	q.Set(Region{1413, 952, 712, 905}, int(37748740))
	q.Set(Region{1600, 751, 2088, 1301}, int(20971521))
	q.Set(Region{0, 0, 1944, 1004}, int(31457286))
	q.Set(Region{343, 338, 804, 484}, int(58720262))
	q.Set(Region{2448, 1213, 1284, 544}, int(50331654))
	q.Set(Region{0, 0, 1204, 584}, int(33554438))
	q.Set(Region{2584, 11, 1254, 684}, int(44040198))
	q.Set(Region{2372, 1188, 1374, 784}, int(29360134))
	q.Set(Region{2364, 14, 1474, 444}, int(39845894))
	q.Set(Region{290, 1497, 804, 484}, int(56623110))
	q.Set(Region{2652, 997, 1774, 644}, int(35651590))
	q.Set(Region{0, 1614, 1444, 544}, int(16777222))
	q.Set(Region{1362, 290, 1653, 1260}, int(20971577))
	q.Set(Region{1228, 352, 1840, 1500}, int(12582932))
	q.Set(Region{2102, 709, 1424, 1024}, int(67108870))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Get(3000, 500)
	}
}
