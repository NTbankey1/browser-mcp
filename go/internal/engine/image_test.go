package engine

import (
	"testing"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/vision"
)

func TestPageOrder(t *testing.T) {
	matches := []vision.Match{
		{X: 300, Y: 40, W: 10, H: 10},
		{X: 20, Y: 200, W: 10, H: 10},
		{X: 10, Y: 40, W: 10, H: 10},
		{X: 100, Y: 40, W: 10, H: 10},
	}
	ordered := pageOrder(matches)
	want := [][2]int{{10, 40}, {100, 40}, {300, 40}, {20, 200}}
	for i, point := range want {
		if ordered[i].X != point[0] || ordered[i].Y != point[1] {
			t.Fatalf("position %d is %d,%d, want %d,%d", i, ordered[i].X, ordered[i].Y, point[0], point[1])
		}
	}
	// The caller's slice keeps its order: the best score still wins when no
	// --nth is given.
	if matches[0].X != 300 {
		t.Fatalf("pageOrder reordered its argument")
	}
}

func TestDescribeElement(t *testing.T) {
	cases := []struct {
		element pointElement
		want    string
	}{
		{pointElement{Tag: "button", ID: "save", Name: "Save"}, `<button#save> "Save"`},
		{pointElement{Tag: "canvas", ID: "paint"}, `<canvas#paint>`},
		{pointElement{Tag: "div"}, `<div>`},
		{pointElement{Frame: "https://example.test/frame"}, `inside <iframe src="https://example.test/frame">; the click goes there`},
		{pointElement{}, "nothing is at that point any more; the page may have changed"},
	}
	for _, c := range cases {
		if got := describeElement(c.element); got != c.want {
			t.Errorf("describeElement(%+v) = %q, want %q", c.element, got, c.want)
		}
	}
}
