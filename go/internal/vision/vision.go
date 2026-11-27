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

// Crop returns the part of the image inside the rectangle, clamped to the
// image. A rectangle with no area left after clamping gives an empty image.
func (im *Image) Crop(x, y, w, h int) *Image {
	left, top := x, y
	right, bottom := x+w, y+h
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	if right > im.W {
		right = im.W
	}
	if bottom > im.H {
		bottom = im.H
	}
	if right <= left || bottom <= top {
		return &Image{}
	}
	out := &Image{W: right - left, H: bottom - top}
	out.Gray = make([]float32, out.W*out.H)
	for row := 0; row < out.H; row++ {
		copy(out.Gray[row*out.W:(row+1)*out.W], im.Gray[(top+row)*im.W+left:(top+row)*im.W+right])
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

// coarseKeep is how many sampled positions survive the coarse pass to be
// refined at their own level. More than candidatesPerScale because a sample a
// pixel off the true peak can score well below it, and overlaps are dropped
// only after refinement.
const coarseKeep = 64

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
	levels := map[int]*Image{}
	level := func(factor int) *Image {
		if cached, ok := levels[factor]; ok {
			return cached
		}
		downsampled := scene.downsampled(factor)
		levels[factor] = downsampled
		return downsampled
	}
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
		for _, candidate := range coarseCandidates(level(factor), scaled.downsampled(factor)) {
			match := refineFrom(level, scaled, candidate, factor)
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

// coarseFactor is how much the scene is downsampled for the first pass: the
// coarse template keeps about 12 px on its short side — enough structure to
// point at the right place (a 6 px icon is a blob, and a blob picks the wrong
// neighbor) — and is a power of two so refineFrom can halve its way back to
// full resolution. At most 16, so a huge template doesn't make the first pass
// crawl.
func coarseFactor(w, h int) int {
	short := w
	if h < short {
		short = h
	}
	target := short / 12
	if target < 1 {
		target = 1
	}
	if target > 16 {
		target = 16
	}
	factor := 1
	for factor*2 <= target {
		factor *= 2
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
	// Every other pixel is enough to find where to look: the best positions
	// are then refined around, at this level, so a peak between two samples is
	// not lost. A coarse template under 8 px has too few samples to stride.
	stride := 2
	if template.W < 8 || template.H < 8 {
		stride = 1
	}
	// The sampled scores are only a guide: a sample next to the true peak can
	// score far lower than the peak between samples, so nothing is suppressed
	// yet. Keep the best sampled positions, refine each, and only then drop the
	// ones that overlap a better one.
	var sampled []candidate
	for y := 0; y <= scene.H-template.H; y += stride {
		for x := 0; x <= scene.W-template.W; x += stride {
			score := correlation(scene, template, x, y)
			if score <= 0 {
				continue
			}
			if len(sampled) >= coarseKeep && score <= sampled[len(sampled)-1].Score {
				continue
			}
			sampled = append(sampled, candidate{X: x, Y: y, Score: score})
			sort.SliceStable(sampled, func(i, j int) bool { return sampled[i].Score > sampled[j].Score })
			if len(sampled) > coarseKeep {
				sampled = sampled[:coarseKeep]
			}
		}
	}
	refined := make([]candidate, 0, len(sampled))
	for _, at := range sampled {
		refined = append(refined, bestAround(scene, template, at, stride))
	}
	sort.SliceStable(refined, func(i, j int) bool { return refined[i].Score > refined[j].Score })
	kept := refined[:0]
	for _, at := range refined {
		overlaps := false
		for _, other := range kept {
			if abs(other.X-at.X) < radius && abs(other.Y-at.Y) < radius {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		kept = append(kept, at)
		if len(kept) >= candidatesPerScale {
			break
		}
	}
	return kept
}

// bestAround refines a coarse candidate over ±radius pixels at its own level,
// so a peak that fell between the strided samples is found.
func bestAround(scene *Image, template *Template, at candidate, radius int) candidate {
	best := at
	maxX, maxY := scene.W-template.W, scene.H-template.H
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			x := clampToRange(at.X+dx, 0, maxX)
			y := clampToRange(at.Y+dy, 0, maxY)
			if score := correlation(scene, template, x, y); score > best.Score {
				best = candidate{X: x, Y: y, Score: score}
			}
		}
	}
	return best
}

// refine searches at full resolution around a coarse candidate and returns its
// best match.
func refineFrom(levels func(int) *Image, template *Template, at candidate, factor int) Match {
	// The candidate's position is where the coarse pass (at `factor`) saw the
	// template; its true position is within one coarse pixel of it.
	best := Match{X: at.X * factor, Y: at.Y * factor, W: template.W, H: template.H, Score: at.Score}
	for level := factor / 2; level >= 1; level /= 2 {
		scene := levels(level)
		scaled := template.downsampled(level)
		maxX := scene.W - scaled.W
		maxY := scene.H - scaled.H
		if maxX < 0 || maxY < 0 {
			continue
		}
		// ±2 of this level's pixels covers the ±1 it was narrowed to at the
		// coarser one, with room for rounding.
		centerX, centerY := best.X/level, best.Y/level
		bestX, bestY, bestScore := centerX, centerY, -1.0
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				x, y := centerX+dx, centerY+dy
				x = clampToRange(x, 0, maxX)
				y = clampToRange(y, 0, maxY)
				if score := correlation(scene, scaled, x, y); score > bestScore {
					bestX, bestY, bestScore = x, y, score
				}
			}
		}
		best = Match{X: bestX * level, Y: bestY * level, W: template.W, H: template.H, Score: bestScore}
	}
	return best
}

func clampToRange(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
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
