package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/vision"
)

// ambiguousMargin: two matches whose scores are this close are the same
// picture in two places as far as the score goes, so the agent has to choose.
const ambiguousMargin = 0.02

// imageOptions are the flags `find --image` and `click --image` share.
type imageOptions struct {
	// Path is the template: a PNG or JPEG file, read through the thin process
	// like an upload, so `~` and the app's roots work.
	Path     string
	MinScore float64
	// Nth picks one of several matches, 1-based; 0 means the best must be
	// unambiguous.
	Nth int
	// In limits the search to one element, which settles the common case of
	// the same icon in a row or a menu; nil searches the viewport.
	In *Target
	// InSource is the --in argument as written, for messages.
	InSource string
}

// readImageOptions reads the shared flags. The caller checks --image first:
// without it these commands take their usual target.
func readImageOptions(c *stepContext) (imageOptions, error) {
	path, _ := c.args.Value("image")
	path = strings.TrimSpace(path)
	if path == "" {
		return imageOptions{}, stepErrorf("--image takes a file: --image <file.png>")
	}
	options := imageOptions{Path: path, MinScore: 0.9}
	if raw, ok := c.args.Value("min-score"); ok {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > 1 {
			return imageOptions{}, stepErrorf("--min-score takes a number above 0 and at most 1, like 0.9")
		}
		options.MinScore = value
	}
	if raw, ok := c.args.Value("nth"); ok {
		nth, err := strconv.Atoi(raw)
		if err != nil || nth < 1 || nth > 5 {
			return imageOptions{}, stepErrorf("--nth takes 1 to 5")
		}
		options.Nth = nth
	}
	if raw, ok := c.args.Value("in"); ok {
		target, err := ParseTarget(raw)
		if err != nil {
			return imageOptions{}, err
		}
		options.In = &target
		options.InSource = raw
	}
	return options, nil
}

// imageHit is where the template was found, and how to click it.
type imageHit struct {
	match vision.Match
	// point is the match's center in the viewport's CSS pixels, which is what
	// Input.dispatchMouseEvent takes.
	point pointer
	// scale is the screenshot's pixels per CSS pixel (a 125% page or a
	// retina screen isn't 1).
	scale float64
}

// viewport is the page's visible area: its size and where it starts in the
// page's coordinates (the screenshot's clip).
type viewport struct {
	Width, Height, ScrollX, ScrollY float64
}

// rect is an element's box in page coordinates (CSS pixels).
type rect struct {
	X, Y, Width, Height float64
}

// pointElement says what sits under a point: the page's own answer, so a
// canvas match still tells the agent it hit a canvas and a selector can take
// over when the element has a name.
type pointElement struct {
	Tag   string `json:"tag"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Frame string `json:"frame"`
}

// locateImage reads the template, captures the viewport and finds the
// template in it.
func locateImage(ctx context.Context, c *stepContext, state *TabState, options imageOptions) (imageHit, error) {
	_, _, data, err := c.session.IO.ReadFile(ctx, options.Path)
	if err != nil {
		return imageHit{}, err
	}
	decoded, _, err := vision.Decode(data)
	if err != nil {
		return imageHit{}, stepErrorf("%s isn't an image find can read: %s", options.Path, err)
	}
	template := vision.TemplateFromImage(decoded)
	if template.Count == 0 {
		return imageHit{}, stepErrorf("%s is fully transparent, so there is nothing to look for", options.Path)
	}
	png, view, err := captureViewport(ctx, state.Tab)
	if err != nil {
		return imageHit{}, err
	}
	sceneImage, _, err := vision.Decode(png)
	if err != nil {
		return imageHit{}, stepErrorf("Couldn't read the screenshot Chrome returned: %s", err)
	}
	scene := vision.FromImage(sceneImage)
	scaleX, scaleY := float64(scene.W)/view.Width, float64(scene.H)/view.Height
	if scaleX <= 0 || scaleY <= 0 {
		return imageHit{}, stepErrorf("The screenshot has no pixels to search")
	}
	// The match is found in the crop when --in is given, so every coordinate
	// gets the crop's origin back before it is turned into a viewport point.
	originX, originY := 0, 0
	if options.In != nil {
		region, err := regionRect(ctx, state, *options.In)
		if err != nil {
			return imageHit{}, err
		}
		left, top := region.X-view.ScrollX, region.Y-view.ScrollY
		if left+region.Width <= 0 || top+region.Height <= 0 || left >= view.Width || top >= view.Height {
			return imageHit{}, stepErrorf("%s isn't on screen; scroll to it, or search without --in", options.InSource)
		}
		cropX := int(math.Round(left * scaleX))
		cropY := int(math.Round(top * scaleY))
		cropW := int(math.Round(region.Width * scaleX))
		cropH := int(math.Round(region.Height * scaleY))
		if cropX < 0 {
			originX = 0
		} else {
			originX = cropX
		}
		if cropY < 0 {
			originY = 0
		} else {
			originY = cropY
		}
		cropped := scene.Crop(cropX, cropY, cropW, cropH)
		if cropped.W == 0 || cropped.H == 0 {
			return imageHit{}, stepErrorf("Nothing of %s is on screen; scroll to it, or search without --in", options.InSource)
		}
		scene = cropped
	}

	matches := vision.Search(scene, template, vision.Options{MaxMatches: 5})
	var passing []vision.Match
	for _, match := range matches {
		if match.Score >= options.MinScore {
			passing = append(passing, match)
		}
	}
	if len(passing) == 0 {
		if len(matches) == 0 {
			return imageHit{}, stepErrorf("The image %s wasn't found on screen. Scroll the page, or take a screenshot to check what it shows", options.Path)
		}
		best := matches[0]
		bestX, bestY := best.Center()
		return imageHit{}, stepErrorf(
			"The image %s wasn't found on screen: the best match scored %.2f at %d,%d, under --min-score %.2f. Scroll the page, take a screenshot to check, or lower --min-score",
			options.Path, best.Score, int((float64(originX)+bestX)/scaleX), int((float64(originY)+bestY)/scaleY), options.MinScore,
		)
	}
	chosen := passing[0]
	ordered := pageOrder(passing)
	if options.Nth > 0 {
		if options.Nth > len(ordered) {
			return imageHit{}, stepErrorf("--nth %d, but the image matches %d places", options.Nth, len(ordered))
		}
		chosen = ordered[options.Nth-1]
	} else if len(passing) > 1 && passing[1].Score >= chosen.Score-ambiguousMargin {
		return imageHit{}, stepErrorf(
			"The image matches %d places%s; choose one with --nth 1-%d (top to bottom, left to right): %s",
			len(passing), inPhrase(options.InSource), len(passing), describePlaces(ordered, originX, originY, scaleX, scaleY),
		)
	}
	centerX, centerY := chosen.Center()
	return imageHit{
		match: chosen,
		point: pointer{X: (float64(originX) + centerX) / scaleX, Y: (float64(originY) + centerY) / scaleY, MainFrame: true},
		scale: scaleX,
	}, nil
}

// inPhrase says where the search was limited, for messages.
func inPhrase(inSource string) string {
	if inSource == "" {
		return ""
	}
	return " in " + inSource
}

// pageOrder sorts matches the way a reader meets them: top to bottom, then
// left to right. --nth counts in this order, so it means the same thing on
// every page; the best score still wins when no --nth is given.
func pageOrder(matches []vision.Match) []vision.Match {
	ordered := append([]vision.Match(nil), matches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Y != ordered[j].Y {
			return ordered[i].Y < ordered[j].Y
		}
		return ordered[i].X < ordered[j].X
	})
	return ordered
}

// describePlaces lists matches as viewport points and scores.
func describePlaces(matches []vision.Match, originX, originY int, scaleX, scaleY float64) string {
	places := make([]string, 0, len(matches))
	for _, match := range matches {
		x, y := match.Center()
		places = append(places, fmt.Sprintf("(%d,%d) %.2f", int((float64(originX)+x)/scaleX), int((float64(originY)+y)/scaleY), match.Score))
	}
	return strings.Join(places, ", ")
}

// regionRect is an element's box in page coordinates, the space the
// screenshot's clip uses.
func regionRect(ctx context.Context, state *TabState, target Target) (rect, error) {
	res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return rect{}, err
	}
	result, err := callFunction(ctx, state.Tab, objectID, `function(){
  const r = this.getBoundingClientRect();
  return { x: r.left + window.scrollX, y: r.top + window.scrollY, width: r.width, height: r.height };
}`, true)
	if err != nil {
		return rect{}, err
	}
	var reply struct {
		Result struct {
			Value rect `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return rect{}, err
	}
	return reply.Result.Value, nil
}

// captureViewport screenshots the visible page losslessly, for matching: the
// screenshot the tool shows a model is a JPEG, which blurs the very pixels
// the comparison needs. The clip is in page coordinates, like the element
// screenshot's.
func captureViewport(ctx context.Context, tab *cdp.Tab) ([]byte, viewport, error) {
	if err := ensureVisible(ctx, tab); err != nil {
		return nil, viewport{}, err
	}
	var view viewport
	if err := tab.Eval(ctx, "({width: innerWidth, height: innerHeight, scrollX: window.scrollX, scrollY: window.scrollY})", &view); err != nil {
		return nil, viewport{}, err
	}
	if view.Width < 1 || view.Height < 1 {
		return nil, viewport{}, stepErrorf("The page has no viewport to search")
	}
	result, err := tab.Send(ctx, "Page.captureScreenshot", map[string]any{
		"format": "png",
		"clip": map[string]any{
			"x": view.ScrollX, "y": view.ScrollY,
			"width": view.Width, "height": view.Height, "scale": 1.0,
		},
	})
	if err != nil {
		return nil, viewport{}, err
	}
	var reply struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return nil, viewport{}, err
	}
	png, err := base64.StdEncoding.DecodeString(reply.Data)
	if err != nil {
		return nil, viewport{}, err
	}
	return png, view, nil
}

// describePoint asks the page what is under a viewport point.
func describePoint(ctx context.Context, tab *cdp.Tab, x, y float64) pointElement {
	expression := fmt.Sprintf(`(() => {
  const el = document.elementFromPoint(%s, %s);
  if (!el) return { tag: "", id: "", name: "", frame: "" };
  const name = (el.getAttribute("aria-label") || el.innerText || el.value || el.getAttribute("alt") || "")
    .replace(/\s+/g, " ").trim().slice(0, 100);
  return {
    tag: el.tagName.toLowerCase(),
    id: el.id || "",
    name,
    frame: el.tagName === "IFRAME" ? (el.src || "") : "",
  };
})()`,
		strconv.FormatFloat(x, 'f', 2, 64),
		strconv.FormatFloat(y, 'f', 2, 64),
	)
	var element pointElement
	if err := tab.Eval(ctx, expression, &element); err != nil {
		return pointElement{}
	}
	return element
}

// describeElement is the "on:" line of a find --image result.
func describeElement(element pointElement) string {
	if element.Frame != "" {
		return fmt.Sprintf("inside <iframe src=%q>; the click goes there", element.Frame)
	}
	if element.Tag == "" {
		return "nothing is at that point any more; the page may have changed"
	}
	description := "<" + element.Tag
	if element.ID != "" {
		description += "#" + element.ID
	}
	description += ">"
	if element.Name != "" {
		description += fmt.Sprintf(" %q", element.Name)
	}
	return description
}

func runFindImage(ctx context.Context, c *stepContext, options imageOptions) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	hit, err := locateImage(ctx, c, state, options)
	if err != nil {
		return err
	}
	x, y := hit.point.X, hit.point.Y
	element := describePoint(ctx, state.Tab, x, y)
	width, height := float64(hit.match.W)/hit.scale, float64(hit.match.H)/hit.scale
	c.Print(fmt.Sprintf(
		"found %s at %d,%d — %d×%d px, score %.2f\non: %s",
		options.Path, int(math.Round(x)), int(math.Round(y)),
		int(math.Round(width)), int(math.Round(height)), hit.match.Score,
		describeElement(element),
	))
	return nil
}

func runClickImage(ctx context.Context, c *stepContext, options imageOptions) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	hit, err := locateImage(ctx, c, state, options)
	if err != nil {
		return err
	}
	clickOptions, err := readClickOptions(c)
	if err != nil {
		return err
	}
	c.Note(fmt.Sprintf("the image is at %d,%d (score %.2f)", int(math.Round(hit.point.X)), int(math.Round(hit.point.Y)), hit.match.Score))
	timedOut, err := settle(ctx, state.Tab, func() error { return click(ctx, state.Tab, hit.point, clickOptions) })
	if err != nil {
		return err
	}
	if timedOut {
		c.Note("the page is still loading")
	}
	return nil
}
