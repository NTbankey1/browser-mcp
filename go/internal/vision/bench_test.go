package vision

import (
	"image"
	"image/color"
	"testing"
)

// card draws a 320×220 picture, the size of a dialog or a canvas.
func card(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			value := uint8(245)
			if x > 20 && x < w-20 && y > 20 && y < h-20 {
				value = 60
			}
			if (x/17+y/11)%2 == 0 && x > w/2 {
				value = 180
			}
			img.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return img
}

func benchmarkSearch(b *testing.B, sceneWidth, sceneHeight int, template *image.Gray, stamped bool) {
	scene := background(sceneWidth, sceneHeight)
	if stamped {
		stamp(scene, template, sceneWidth/3, sceneHeight/3)
	}
	reference := FromImage(scene)
	search := TemplateFromImage(template)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if matches := Search(reference, search, Options{}); stamped && len(matches) == 0 {
			b.Fatal("no match")
		}
	}
}

func BenchmarkSearchIcon(b *testing.B) {
	benchmarkSearch(b, 1280, 900, icon(24), true)
}

func BenchmarkSearchIconNoMatch(b *testing.B) {
	benchmarkSearch(b, 1280, 900, icon(24), false)
}

func BenchmarkSearchCard(b *testing.B) {
	benchmarkSearch(b, 1280, 900, card(320, 220), true)
}

func BenchmarkSearchCardNoMatch(b *testing.B) {
	benchmarkSearch(b, 1280, 900, card(320, 220), false)
}

func BenchmarkSearchMaskedIcon(b *testing.B) {
	size := 24
	picture := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			if dx*dx+dy*dy < (size/3)*(size/3) {
				picture.SetNRGBA(x, y, color.NRGBA{R: 30, G: 200, B: 90, A: 255})
			}
		}
	}
	scene := background(1280, 900)
	stamp(scene, icon(size), 400, 300)
	reference := FromImage(scene)
	search := TemplateFromImage(picture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Search(reference, search, Options{})
	}
}
