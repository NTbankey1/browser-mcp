package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

// icon draws a small, distinctive picture: a light disc, dark stripes on its
// right half, over a dark square.
func icon(size int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			value := uint8(40)
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy < (size/3)*(size/3) {
				value = 225
			}
			if x > size/2 && (x/3+y/3)%2 == 0 {
				value = 100
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return img
}

// background is a scene that varies, so no flat area can pose as a match.
func background(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			value := uint8(60 + (x/9+y/13)%5*8)
			if (x/40+y/40)%2 == 0 {
				value = uint8(150 + (x/7+y/5)%4*6)
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return img
}

// stamp copies a picture into a scene.
func stamp(scene *image.Gray, picture *image.Gray, ox, oy int) {
	bounds := picture.Bounds()
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			scene.SetGray(ox+x, oy+y, picture.GrayAt(x, y))
		}
	}
}

// crop copies a rectangle of a scene.
func crop(scene *image.Gray, x, y, w, h int) *image.Gray {
	out := image.NewGray(image.Rect(0, 0, w, h))
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			out.SetGray(dx, dy, scene.GrayAt(x+dx, y+dy))
		}
	}
	return out
}

// scale resizes a picture by nearest neighbor, as a screenshot of a zoomed
// page would show it.
func scale(picture *image.Gray, w, h int) *image.Gray {
	out := image.NewGray(image.Rect(0, 0, w, h))
	bounds := picture.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := x * bounds.Dx() / w
			sy := y * bounds.Dy() / h
			out.SetGray(x, y, picture.GrayAt(sx, sy))
		}
	}
	return out
}

func search(img *image.Gray, template *image.Gray) []Match {
	return Search(FromImage(img), TemplateFromImage(template), Options{MaxMatches: 5})
}

func TestFindsTheTemplateWhereItWasStamped(t *testing.T) {
	scene := background(240, 180)
	picture := icon(22)
	stamp(scene, picture, 60, 40)

	matches := search(scene, picture)
	if len(matches) == 0 {
		t.Fatal("no match")
	}
	best := matches[0]
	if best.Score < 0.99 {
		t.Fatalf("score %.3f at %d,%d: expected a near-exact match", best.Score, best.X, best.Y)
	}
	if best.X != 60 || best.Y != 40 {
		t.Fatalf("match at %d,%d, stamped at 60,40", best.X, best.Y)
	}
}

func TestFindsATemplateAtAnotherScale(t *testing.T) {
	picture := icon(20)
	zoomed := scale(picture, 25, 25)
	scene := background(240, 180)
	stamp(scene, zoomed, 40, 30)

	matches := search(scene, picture)
	if len(matches) == 0 {
		t.Fatal("no match")
	}
	best := matches[0]
	if math.Abs(best.Scale-1.25) > 0.01 {
		t.Fatalf("scale %.2f, expected 1.25", best.Scale)
	}
	cx, cy := best.Center()
	if math.Abs(cx-52.5) > 2 || math.Abs(cy-42.5) > 2 {
		t.Fatalf("center %.0f,%.0f, expected about 52,42", cx, cy)
	}
	if best.Score < 0.8 {
		t.Fatalf("score %.3f at the right scale", best.Score)
	}
}

func TestNoMatchOutsideTheScene(t *testing.T) {
	scene := background(240, 180)
	matches := search(scene, icon(22))
	if len(matches) > 0 && matches[0].Score >= 0.9 {
		t.Fatalf("score %.3f for a template that isn't there", matches[0].Score)
	}
}

func TestTwoCopiesAreTwoMatches(t *testing.T) {
	scene := background(300, 200)
	picture := icon(24)
	stamp(scene, picture, 30, 30)
	stamp(scene, picture, 200, 120)

	matches := search(scene, picture)
	if len(matches) < 2 {
		t.Fatalf("%d matches, expected two", len(matches))
	}
	if matches[0].Score < 0.99 || matches[1].Score < 0.99 {
		t.Fatalf("scores %.3f and %.3f", matches[0].Score, matches[1].Score)
	}
	x1, y1 := matches[0].Center()
	x2, y2 := matches[1].Center()
	if math.Abs(x1-42)+math.Abs(y1-42) > 4 && math.Abs(x2-42)+math.Abs(y2-42) > 4 {
		t.Fatalf("neither match is at the first copy: %.0f,%.0f and %.0f,%.0f", x1, y1, x2, y2)
	}
}

// A PNG cut out of a screenshot (transparent around the icon) matches over a
// different background: only the opaque pixels count.
func TestTransparentPixelsAreAMask(t *testing.T) {
	size := 24
	disc := icon(size)
	picture := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy < (size/3)*(size/3) {
				value := disc.GrayAt(x, y).Y
				picture.SetNRGBA(x, y, color.NRGBA{R: value, G: value, B: value, A: 255})
			} else {
				// Transparent, and black where it is: the mask keeps it out
				picture.SetNRGBA(x, y, color.NRGBA{A: 0})
			}
		}
	}
	scene := image.NewGray(image.Rect(0, 0, 200, 140))
	for y := 0; y < 140; y++ {
		for x := 0; x < 200; x++ {
			scene.SetGray(x, y, color.Gray{Y: 235}) // a white page, not black
		}
	}
	// The icon, in the color it has on this page
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy < (size/3)*(size/3) {
				scene.SetGray(80+x, 50+y, disc.GrayAt(x, y))
			}
		}
	}

	matches := Search(FromImage(scene), TemplateFromImage(picture), Options{})
	if len(matches) == 0 {
		t.Fatal("no match")
	}
	if matches[0].Score < 0.9 {
		t.Fatalf("score %.3f: the transparent surroundings were counted", matches[0].Score)
	}
	if matches[0].X != 80 || matches[0].Y != 50 {
		t.Fatalf("match at %d,%d, expected 80,50", matches[0].X, matches[0].Y)
	}
}

func TestCrop(t *testing.T) {
	scene := FromImage(background(40, 30))
	cropped := scene.Crop(10, 5, 20, 10)
	if cropped.W != 20 || cropped.H != 10 {
		t.Fatalf("crop is %d×%d, expected 20×10", cropped.W, cropped.H)
	}
	if cropped.Gray[0] != scene.Gray[5*40+10] {
		t.Fatalf("crop starts at %v, expected %v", cropped.Gray[0], scene.Gray[5*40+10])
	}
	clamped := scene.Crop(30, 25, 100, 100)
	if clamped.W != 10 || clamped.H != 5 {
		t.Fatalf("clamped crop is %d×%d, expected 10×5", clamped.W, clamped.H)
	}
	if empty := scene.Crop(50, 50, 10, 10); empty.W != 0 || empty.H != 0 {
		t.Fatalf("crop outside the image is %d×%d, expected empty", empty.W, empty.H)
	}
}

// Normalized correlation ignores a change of brightness and contrast: a
// template pulled from one theme still finds the control in another, as long
// as the colors keep their order.
func TestBrighterAndDarkerSceneStillMatches(t *testing.T) {
	picture := icon(22)
	scene := background(200, 150)
	darker := image.NewGray(picture.Bounds())
	for y := 0; y < picture.Bounds().Dy(); y++ {
		for x := 0; x < picture.Bounds().Dx(); x++ {
			value := float64(picture.GrayAt(x, y).Y)
			darker.SetGray(x, y, color.Gray{Y: uint8(value*0.6 + 25)})
		}
	}
	stamp(scene, darker, 70, 50)

	matches := search(scene, picture)
	if len(matches) == 0 || matches[0].Score < 0.95 {
		t.Fatalf("%d matches, best %.3f: a darker copy should still match", len(matches), bestScore(matches))
	}
	if matches[0].X != 70 || matches[0].Y != 50 {
		t.Fatalf("match at %d,%d, expected 70,50", matches[0].X, matches[0].Y)
	}
}

// A control next to where the template was captured is found where it is now,
// to the pixel.
func TestFindsAControlOnePixelAway(t *testing.T) {
	picture := icon(20)
	scene := background(200, 150)
	stamp(scene, picture, 61, 41)

	matches := search(scene, picture)
	if len(matches) == 0 {
		t.Fatal("no match")
	}
	if matches[0].X != 61 || matches[0].Y != 41 {
		t.Fatalf("match at %d,%d, expected 61,41", matches[0].X, matches[0].Y)
	}
}

// Templates are usually JPEGs, since that is what `screenshot --path` writes.
func TestJPEGTemplateMatches(t *testing.T) {
	picture := icon(24)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	decoded, _, err := Decode(encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	scene := background(240, 180)
	stamp(scene, picture, 90, 60)

	matches := Search(FromImage(scene), TemplateFromImage(decoded), Options{})
	if len(matches) == 0 || matches[0].Score < 0.9 {
		t.Fatalf("%d matches, best %.3f for a JPEG template", len(matches), bestScore(matches))
	}
	if matches[0].X < 89 || matches[0].X > 91 || matches[0].Y < 59 || matches[0].Y > 61 {
		t.Fatalf("match at %d,%d, expected about 90,60", matches[0].X, matches[0].Y)
	}
}

// A tiny template (a 6×6 badge) is still found where it was stamped.
func TestTinyTemplate(t *testing.T) {
	picture := image.NewGray(image.Rect(0, 0, 6, 6))
	values := []uint8{10, 240, 30, 200, 60, 160, 90, 120, 100, 220, 140, 20, 180, 40, 250, 80, 210, 50}
	for i := range picture.Pix {
		picture.Pix[i] = values[i%len(values)]
	}
	scene := background(120, 90)
	stamp(scene, picture, 40, 30)

	matches := search(scene, picture)
	if len(matches) == 0 || matches[0].Score < 0.9 {
		t.Fatalf("%d matches, best %.3f for a 6 px template", len(matches), bestScore(matches))
	}
	if matches[0].X != 40 || matches[0].Y != 30 {
		t.Fatalf("match at %d,%d, expected 40,30", matches[0].X, matches[0].Y)
	}
}

func TestTemplateBiggerThanTheScene(t *testing.T) {
	scene := FromImage(background(100, 80))
	template := TemplateFromImage(card(300, 200))
	if matches := Search(scene, template, Options{}); len(matches) != 0 {
		t.Fatalf("%d matches for a template larger than the scene", len(matches))
	}
}

func bestScore(matches []Match) float64 {
	if len(matches) == 0 {
		return 0
	}
	return matches[0].Score
}

// A flat template would score 1 against every flat area; it scores 0 instead.
func TestFlatTemplateMatchesNothing(t *testing.T) {
	flat := image.NewGray(image.Rect(0, 0, 20, 20))
	for i := range flat.Pix {
		flat.Pix[i] = 120
	}
	scene := background(120, 90)
	for _, match := range search(scene, flat) {
		if match.Score > 0 {
			t.Fatalf("flat template scored %.3f at %d,%d", match.Score, match.X, match.Y)
		}
	}
}
