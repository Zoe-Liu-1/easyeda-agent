package app

import (
	"math"
	"testing"
)

func TestDCFindOrthogonalPathSnapsFinalConnectionExactly(t *testing.T) {
	start := [2]float64{0, 0}
	end := [2]float64{100.3, 50.4}
	clear := func(a, b [2]float64) bool { return true }
	path, ok := dcFindOrthogonalPath(start, end, nil, nil, clear, 300)
	if !ok || len(path) < 2 {
		t.Fatalf("no path: %+v", path)
	}
	if math.Abs(path[0][0]-start[0]) > 1e-6 || math.Abs(path[0][1]-start[1]) > 1e-6 {
		t.Fatalf("wrong start: %+v", path)
	}
	last := path[len(path)-1]
	if math.Abs(last[0]-end[0]) > 1e-6 || math.Abs(last[1]-end[1]) > 1e-6 {
		t.Fatalf("wrong end: %+v", path)
	}
	for i := 0; i+1 < len(path); i++ {
		if math.Abs(path[i][0]-path[i+1][0]) > 1e-6 &&
			math.Abs(path[i][1]-path[i+1][1]) > 1e-6 {
			t.Fatalf("non-orthogonal segment %d: %+v", i, path)
		}
	}
}
