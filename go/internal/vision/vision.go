// Package vision finds a small reference image — a template — in a screenshot:
// template matching, the idea behind Sikuli. It lets an agent find and click
// controls that selectors cannot reach (a canvas, a closed shadow root, a
// cross-origin frame) or verify that something is on screen without knowing
// its DOM.
//
// The score is normalized cross-correlation over grayscale pixels, which
// tolerates a change of brightness or contrast (a hover state, a theme with the
// same colors) but not a change of hue or an inverted palette. A PNG template's
// transparent pixels are a mask: only the opaque ones take part, so an icon cut
// out of a screenshot matches over any background.
package vision

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"
)

// Image is a grayscale picture, values 0..1, row-major.
type Image struct {
	W, H int
	Gray []float32
}

// Decode reads a PNG or JPEG image.
func Decode(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("not a PNG or JPEG image: %w", err)
	}
	return img, format, nil
}

// FromImage converts a decoded image to grayscale.
func FromImage(img image.Image) *Image {
	bounds := img.Bounds()
	out := &Image{W: bounds.Dx(), H: bounds.Dy()}
	out.Gray = make([]float32, out.W*out.H)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			out.Gray[y*out.W+x] = luma(img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return out
}

// luma is Rec. 709 luminance, the weights Chrome's screenshots are built from.
func luma(c color.Color) float32 {
	r, g, b, _ := c.RGBA()
	return float32(0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b)) / 65535
}

func alphaAt(c color.Color) uint8 {
	return color.NRGBAModel.Convert(c).(color.NRGBA).A
}

// Template is a reference image, with an optional mask of the pixels that count.
type Template struct {
	W, H int
	Gray []float32
	// Mask is nil when every pixel counts; otherwise true means the pixel is
	// part of the picture (a transparent pixel of a PNG is not).
	Mask  []bool
	Count int
	Mean  float64
	Var   float64
}

// TemplateFromImage reads a template, taking a PNG's transparency as a mask.
func TemplateFromImage(img image.Image) *Template {
	bounds := img.Bounds()
	t := &Template{W: bounds.Dx(), H: bounds.Dy()}
	t.Gray = make([]float32, t.W*t.H)
	opaque := make([]bool, t.W*t.H)
	anyTransparent := false
	for y := 0; y < t.H; y++ {
		for x := 0; x < t.W; x++ {
			pixel := img.At(bounds.Min.X+x, bounds.Min.Y+y)
			t.Gray[y*t.W+x] = luma(pixel)
			visible := alphaAt(pixel) >= 250
			opaque[y*t.W+x] = visible
			if !visible {
				anyTransparent = true
			}
		}
	}
	if anyTransparent {
		t.Mask = opaque
	}
	t.measure()
	return t
}

// measure computes the mean and variance over the counted pixels.
func (t *Template) measure() {
	var sum, sumOfSquares float64
	count := 0
	for i, value := range t.Gray {
		if t.Mask != nil && !t.Mask[i] {
			continue
		}
		v := float64(value)
		sum += v
		sumOfSquares += v * v
		count++
	}
	t.Count = count
	if count == 0 {
		t.Mean, t.Var = 0, 0
		return
	}
	mean := sum / float64(count)
	t.Mean = mean
	t.Var = math.Max(0, sumOfSquares/float64(count)-mean*mean)
}

// resized scales the template to w×h pixels. Masked (transparent) pixels are
// resampled as well, so the mask follows the picture.
func (t *Template) resized(w, h int) *Template {
	if w == t.W && h == t.H {
		return t
	}
	out := &Template{W: w, H: h, Gray: make([]float32, w*h)}
	if t.Mask == nil {
		for y := 0; y < h; y++ {
			sy := (float64(y)+0.5)*float64(t.H)/float64(h) - 0.5
			for x := 0; x < w; x++ {
				sx := (float64(x)+0.5)*float64(t.W)/float64(w) - 0.5
				out.Gray[y*w+x] = bilinear(t.Gray, t.W, t.H, sx, sy)
			}
		}
		out.measure()
		return out
	}
	mask := make([]bool, w*h)
	for y := 0; y < h; y++ {
		sy := (float64(y)+0.5)*float64(t.H)/float64(h) - 0.5
		for x := 0; x < w; x++ {
			sx := (float64(x)+0.5)*float64(t.W)/float64(w) - 0.5
			out.Gray[y*w+x] = bilinear(t.Gray, t.W, t.H, sx, sy)
			mask[y*w+x] = nearest(t.Mask, t.W, t.H, sx, sy)
		}
	}
	out.Mask = mask
	out.measure()
	return out
}

// downsampled box-filters the template by factor d (d ≥ 2).
func (t *Template) downsampled(d int) *Template {
	if d <= 1 {
		return t
	}
	w, h := t.W/d, t.H/d
	if w < 1 || h < 1 {
		return t
	}
	out := &Template{W: w, H: h, Gray: make([]float32, w*h)}
	if t.Mask == nil {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sum float32
				for dy := 0; dy < d; dy++ {
					row := t.Gray[(y*d+dy)*t.W+x*d:]
					for _, v := range row[:d] {
						sum += v
					}
				}
				out.Gray[y*w+x] = sum / float32(d*d)
			}
		}
		out.measure()
		return out
	}
	mask := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float32
			opaque := 0
			for dy := 0; dy < d; dy++ {
				for dx := 0; dx < d; dx++ {
					index := (y*d+dy)*t.W + x*d + dx
					sum += t.Gray[index]
					if t.Mask[index] {
						opaque++
					}
				}
			}
			out.Gray[y*w+x] = sum / float32(d*d)
			// A coarse pixel counts when most of it is part of the picture.
			mask[y*w+x] = opaque*2 > d*d
		}
	}
	out.Mask = mask
	out.measure()
	return out
}

// downsampled box-filters the image by factor d (d ≥ 2).
func (im *Image) downsampled(d int) *Image {
	if d <= 1 {
		return im
	}
	w, h := im.W/d, im.H/d
	if w < 1 || h < 1 {
		return im
	}
	out := &Image{W: w, H: h, Gray: make([]float32, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float32
			for dy := 0; dy < d; dy++ {
				row := im.Gray[(y*d+dy)*im.W+x*d:]
				for _, v := range row[:d] {
					sum += v
				}
			}
			out.Gray[y*w+x] = sum / float32(d*d)
		}
	}
	return out
}

func bilinear(pixels []float32, w, h int, x, y float64) float32 {
	x = clampCoordinate(x, w)
	y = clampCoordinate(y, h)
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := x0+1, y0+1
	if x1 >= w {
		x1 = w - 1
	}
	if y1 >= h {
		y1 = h - 1
	}
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	top := pixels[y0*w+x0]*(1-fx) + pixels[y0*w+x1]*fx
	bottom := pixels[y1*w+x0]*(1-fx) + pixels[y1*w+x1]*fx
	return top*(1-fy) + bottom*fy
}

func nearest(mask []bool, w, h int, x, y float64) bool {
	x = clampCoordinate(x, w)
	y = clampCoordinate(y, h)
	return mask[int(math.Round(y))*w+int(math.Round(x))]
}

func clampCoordinate(value float64, size int) float64 {
	if value < 0 {
		return 0
	}
	if value > float64(size-1) {
		return float64(size - 1)
	}
	return value
}

// Options adjust a search.
type Options struct {
	// MaxMatches caps the matches returned (default 5).
	MaxMatches int
	// Scales are the sizes tried, as multiples of the template's own size
	// (default: 1, then common zoom and pixel ratios). The first scale with a
	// near-exact match ends the search.
	Scales []float64
}

// Match is one place the template matches.
type Match struct {
	X, Y, W, H int
	Score      float64
	Scale      float64
}

// Center is the middle of the match, in the scene's pixels.
func (m Match) Center() (float64, float64) {
	return float64(m.X) + float64(m.W)/2, float64(m.Y) + float64(m.H)/2
}

// DefaultScales: 1, then the common zoom levels and pixel ratios (125%, 150%,
// 200%, 80%, 67%, 50%). 1 comes first because most templates are captured from
// the same screen they are searched on.
var DefaultScales = []float64{1, 1.25, 1.5, 2, 0.8, 2.0 / 3, 0.5}

// candidatesPerScale is how many coarse positions are refined at full resolution.
const candidatesPerScale = 8

// Search looks for the template in the scene and returns up to
// options.MaxMatches matches, best first. It does not apply a threshold: the
// caller decides what counts as a match, and a score below it is still useful
// to say how close the best attempt was.
func Search(scene *Image, template *Template, options Options) []Match {
	if scene == nil || template == nil || template.Count == 0 || scene.W < 1 || scene.H < 1 {
		return nil
	}
	maxMatches := options.MaxMatches
	if maxMatches <= 0 {
		maxMatches = 5
	}
	scales := options.Scales
	if len(scales) == 0 {
		scales = DefaultScales
	}
	coarseScenes := map[int]*Image{}
	var found []Match
	for _, scale := range scales {
		if scale <= 0 {
			continue
		}
		w := int(math.Round(float64(template.W) * scale))
		h := int(math.Round(float64(template.H) * scale))
		if w < 3 || h < 3 || w > scene.W || h > scene.H {
			continue
		}
		scaled := template.resized(w, h)
		factor := coarseFactor(w, h)
		coarse, ok := coarseScenes[factor]
		if !ok {
			coarse = scene.downsampled(factor)
			coarseScenes[factor] = coarse
		}
		for _, candidate := range coarseCandidates(coarse, scaled.downsampled(factor)) {
			match := refine(scene, scaled, candidate, factor)
			match.Scale = float64(w) / float64(template.W)
			if match.Score > 0 {
				found = append(found, match)
			}
		}
		found = suppress(found, 0)
		if len(found) > 0 && found[0].Score >= 0.995 {
			// The template matches as it was captured; no other scale can beat it.
			break
		}
	}
	found = suppress(found, maxMatches)
	sort.SliceStable(found, func(i, j int) bool { return found[i].Score > found[j].Score })
	if len(found) > maxMatches {
		found = found[:maxMatches]
	}
	return found
}

// coarseFactor is how much the scene is downsampled for the first pass: enough
// that the template is still recognizable (at least 12 px on its short side),
// at most 4.
func coarseFactor(w, h int) int {
	short := w
	if h < short {
		short = h
	}
	factor := short / 12
	if factor < 1 {
		factor = 1
	}
	if factor > 4 {
		factor = 4
	}
	return factor
}

type candidate struct {
	X, Y  int
	Score float64
}

// coarseCandidates scores every position at the coarse scale and keeps the
// best ones, at most one per neighborhood (the radius is half the template, so
// two distinct copies of the same icon both survive).
func coarseCandidates(scene *Image, template *Template) []candidate {
	if template.W > scene.W || template.H > scene.H {
		return nil
	}
	radius := template.W
	if template.H < radius {
		radius = template.H
	}
	radius /= 2
	if radius < 2 {
		radius = 2
	}
	var best []candidate
	for y := 0; y <= scene.H-template.H; y++ {
		for x := 0; x <= scene.W-template.W; x++ {
			score := correlation(scene, template, x, y)
			if score <= 0 {
				continue
			}
			// A kept candidate in the same neighborhood only blocks a weaker
			// one: a better score replaces it, so the true peak of an area is
			// not suppressed by a neighbor seen earlier.
			blocked := false
			for _, kept := range best {
				if abs(kept.X-x) < radius && abs(kept.Y-y) < radius && kept.Score >= score {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
			kept := best[:0]
			for _, other := range best {
				if abs(other.X-x) < radius && abs(other.Y-y) < radius {
					continue
				}
				kept = append(kept, other)
			}
			best = append(kept, candidate{X: x, Y: y, Score: score})
			sort.SliceStable(best, func(i, j int) bool { return best[i].Score > best[j].Score })
			if len(best) > candidatesPerScale {
				best = best[:candidatesPerScale]
			}
		}
	}
	return best
}

// refine searches at full resolution around a coarse candidate and returns its
// best match.
func refine(scene *Image, template *Template, at candidate, factor int) Match {
	best := Match{X: at.X * factor, Y: at.Y * factor, W: template.W, H: template.H}
	radius := factor
	if radius < 1 {
		radius = 1
	}
	maxX := scene.W - template.W
	maxY := scene.H - template.H
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			x, y := at.X*factor+dx, at.Y*factor+dy
			if x < 0 {
				x = 0
			}
			if y < 0 {
				y = 0
			}
			if x > maxX {
				x = maxX
			}
			if y > maxY {
				y = maxY
			}
			score := correlation(scene, template, x, y)
			if score > best.Score {
				best = Match{X: x, Y: y, W: template.W, H: template.H, Score: score}
			}
		}
	}
	return best
}

// correlation is normalized cross-correlation of the template with the scene
// window at x, y: 1 for the same picture, 0 for unrelated ones.
func correlation(scene *Image, template *Template, x, y int) float64 {
	var sum, sumOfSquares, product float64
	count := 0
	if template.Mask == nil {
		for ty := 0; ty < template.H; ty++ {
			sceneRow := scene.Gray[(y+ty)*scene.W+x:]
			templateRow := template.Gray[ty*template.W:]
			for tx := 0; tx < template.W; tx++ {
				a, b := float64(sceneRow[tx]), float64(templateRow[tx])
				sum += a
				sumOfSquares += a * a
				product += a * b
			}
		}
		count = template.W * template.H
	} else {
		for ty := 0; ty < template.H; ty++ {
			sceneRow := scene.Gray[(y+ty)*scene.W+x:]
			templateRow := template.Gray[ty*template.W:]
			maskRow := template.Mask[ty*template.W:]
			for tx := 0; tx < template.W; tx++ {
				if !maskRow[tx] {
					continue
				}
				a, b := float64(sceneRow[tx]), float64(templateRow[tx])
				sum += a
				sumOfSquares += a * a
				product += a * b
				count++
			}
		}
	}
	if count < 2 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumOfSquares/float64(count) - mean*mean
	if variance <= 1e-8 || template.Var <= 1e-8 {
		// A flat window matches nothing: every flat area would score 1.
		return 0
	}
	score := (product/float64(count) - mean*template.Mean) / math.Sqrt(variance*template.Var)
	if score > 1 {
		return 1
	}
	if score < 0 {
		return 0
	}
	return score
}

// suppress drops matches that overlap a better one (the same place found at
// another scale, or the coarse pass's neighbor). With limit > 0 it also caps
// the result.
func suppress(matches []Match, limit int) []Match {
	if len(matches) < 2 {
		return matches
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	var kept []Match
	for _, match := range matches {
		overlaps := false
		for _, other := range kept {
			short := match.W
			if match.H < short {
				short = match.H
			}
			if other.W < short {
				short = other.W
			}
			if other.H < short {
				short = other.H
			}
			radius := float64(short) / 2
			if radius < 2 {
				radius = 2
			}
			x1, y1 := match.Center()
			x2, y2 := other.Center()
			if dx, dy := x1-x2, y1-y2; math.Hypot(dx, dy) < radius {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, match)
		}
		if limit > 0 && len(kept) >= limit {
			break
		}
	}
	return kept
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
