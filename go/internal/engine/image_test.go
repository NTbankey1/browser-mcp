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

func TestReadImageOptions(t *testing.T) {
	cases := []struct {
		values  map[string]string
		wantMin float64
		wantNth int
		wantIn  bool
		wantErr bool
	}{
		{values: map[string]string{"image": "a.png"}, wantMin: 0.9},
		{values: map[string]string{"image": "a.png", "min-score": "0.75"}, wantMin: 0.75},
		{values: map[string]string{"image": "a.png", "nth": "3"}, wantMin: 0.9, wantNth: 3},
		{values: map[string]string{"image": "a.png", "in": "#paint"}, wantMin: 0.9, wantIn: true},
		{values: map[string]string{"image": ""}, wantErr: true},
		{values: map[string]string{"image": "a.png", "min-score": "0"}, wantErr: true},
		{values: map[string]string{"image": "a.png", "min-score": "1.5"}, wantErr: true},
		{values: map[string]string{"image": "a.png", "nth": "0"}, wantErr: true},
		{values: map[string]string{"image": "a.png", "nth": "9"}, wantErr: true},
	}
	for _, c := range cases {
		step := &stepContext{args: ParsedArgs{Values: c.values, Switches: map[string]bool{}}}
		got, err := readImageOptions(step)
		if c.wantErr {
			if err == nil {
				t.Errorf("%v: expected an error", c.values)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.values, err)
			continue
		}
		if got.MinScore != c.wantMin || got.Nth != c.wantNth || (got.In != nil) != c.wantIn {
			t.Errorf("%v: got min-score %.2f, nth %d, in %v", c.values, got.MinScore, got.Nth, got.In != nil)
		}
		if c.wantIn && got.InSource != c.values["in"] {
			t.Errorf("%v: in source %q", c.values, got.InSource)
		}
	}
}

func TestDescribePlacesAddsTheCropOrigin(t *testing.T) {
	places := describePlaces([]vision.Match{{X: 100, Y: 200, W: 10, H: 10, Score: 0.95}}, 40, 60, 1, 1)
	if places != "(145,265) 0.95" {
		t.Fatalf("places = %q", places)
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
