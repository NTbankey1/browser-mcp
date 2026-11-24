package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
)

// Timing mirrors the TypeScript engine.
const (
	FindTimeout       = 5 * time.Second
	ActionableTimeout = 3 * time.Second
	NavigationStart   = 200 * time.Millisecond
	NavigationLoad    = 10 * time.Second
	ScreenMaxSide     = 1568
	DownloadMaxBytes  = 25 * 1024 * 1024
	TypeKeysMaxChars  = 200
)

// HiddenTabMessage explains why input to a background tab fails.
const HiddenTabMessage = "Chrome isn't rendering this background tab, so it can't take clicks, keys or screenshots. Read it with `snapshot` or `get text`, or ask the user to switch to the tab"

// ensureVisible turns focus emulation on for a hidden tab and fails rather
// than send input that would land later.
func ensureVisible(ctx context.Context, tab *cdp.Tab) error {
	visible, err := tabVisible(ctx, tab)
	if err == nil && visible {
		return nil
	}
	_, _ = tab.Send(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true})
	for i := 0; i < 10; i++ {
		if visible, err := tabVisible(ctx, tab); err == nil && visible {
			return nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return stepErrorf(HiddenTabMessage)
}

func tabVisible(ctx context.Context, tab *cdp.Tab) (bool, error) {
	var visible bool
	err := tab.Eval(ctx, "document.visibilityState === 'visible'", &visible)
	return visible, err
}

// emulateFocus turns rendering on for a claimed tab without taking the user's
// focus.
func emulateFocus(ctx context.Context, tab *cdp.Tab) {
	_, _ = tab.Send(ctx, "Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true})
}

type actionability struct {
	OK       bool    `json:"ok"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Reason   string  `json:"reason"`
	By       string  `json:"by"`
	Retarget bool    `json:"retargeted"`
}

// actionabilityScript is Playwright's actionability check for pointer input:
// scroll into view, visible, enabled, and hit-test the center. A visually
// hidden checkbox, radio or file input is retargeted to its visible label.
const actionabilityScript = `function(){
  const el = this;
  const input = /** @type {HTMLInputElement} */ (el);
  const initial = el.getBoundingClientRect();
  const style0 = getComputedStyle(el);
  const hidden0 = initial.width === 0 || initial.height === 0 || style0.visibility === 'hidden' || style0.opacity === '0';
  let target = el;
  if (hidden0 && input.labels && input.labels.length) {
    for (const label of input.labels) {
      if (label.getBoundingClientRect().width > 0) { target = label; break; }
    }
  }
  const view = target.ownerDocument.defaultView || window;
  let box = target.getBoundingClientRect();
  if (box.bottom <= 0 || box.right <= 0 || box.top >= view.innerHeight || box.left >= view.innerWidth) {
    target.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
    box = target.getBoundingClientRect();
  }
  const style = view.getComputedStyle(target);
  if (box.width === 0 || box.height === 0 || style.visibility === 'hidden') return { ok: false, reason: 'hidden' };
  if (target.disabled || target.getAttribute('aria-disabled') === 'true') return { ok: false, reason: 'disabled' };
  const x = box.left + Math.min(box.width, view.innerWidth - box.left) / 2;
  const y = box.top + Math.min(box.height, view.innerHeight - box.top) / 2;
  if (x < 0 || y < 0) return { ok: false, reason: 'outside' };
  let hit = target.ownerDocument.elementFromPoint(x, y);
  while (hit && hit.shadowRoot) {
    const inner = hit.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === hit) break;
    hit = inner;
  }
  const within = node => {
    for (let n = node; n; n = n.parentNode || (n.host || null)) { if (n === target) return true; }
    return false;
  };
  let mainFrame = true;
  try { mainFrame = target.ownerDocument === target.ownerDocument.defaultView.top.document; } catch (error) { mainFrame = false; }
  if (!hit || within(hit)) return { ok: true, x, y, mainFrame, retargeted: target !== el };
  if (input.labels) {
    for (const label of input.labels) {
      if (label === hit || label.contains(hit)) return { ok: true, x, y, mainFrame, retargeted: target !== el };
    }
  }
  const describe = node => {
    const id = node.id ? '#' + node.id : '';
    const cls = !id && typeof node.className === 'string' && node.className.trim()
      ? '.' + node.className.trim().split(/\s+/)[0] : '';
    return '<' + node.tagName.toLowerCase() + id + cls + '>';
  };
  return { ok: false, reason: 'covered', by: describe(hit) };
}`

// pointer is a resolved element and the viewport point to use.
type pointer struct {
	ObjectID  string
	X, Y      float64
	MainFrame bool
}

// preparePointer waits until an element can take pointer input.
func preparePointer(ctx context.Context, res Resolver, target Target) (pointer, error) {
	tab := res.Tab
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return pointer{}, err
	}
	deadline := time.Now().Add(ActionableTimeout)
	for {
		state, err := checkActionable(ctx, tab, objectID)
		if err != nil {
			return pointer{}, err
		}
		if state.OK {
			return pointer{ObjectID: objectID, X: state.X, Y: state.Y, MainFrame: true}, nil
		}
		if time.Now().After(deadline) {
			switch state.Reason {
			case "covered":
				return pointer{}, stepErrorf("%s is covered by `%s`. Close or dismiss it first", target.Source, state.By)
			case "disabled":
				return pointer{}, stepErrorf("%s is disabled", target.Source)
			default:
				return pointer{}, stepErrorf("%s isn't visible", target.Source)
			}
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return pointer{}, ctx.Err()
		}
	}
}

func checkActionable(ctx context.Context, tab *cdp.Tab, objectID string) (actionability, error) {
	result, err := callFunction(ctx, tab, objectID, actionabilityScript, true)
	if err != nil {
		return actionability{}, err
	}
	var reply struct {
		Result struct {
			Value actionability `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return actionability{}, err
	}
	state := reply.Result.Value
	// A frame's own viewport point is not the tab's; native input uses the
	// main frame's, which only main-frame elements have.
	return state, nil
}

// ClickOptions mirror `click`'s flags.
type ClickOptions struct {
	Button     string
	ClickCount int
	Modifiers  []string
}

// click sends real mouse input at the element's center.
func click(ctx context.Context, tab *cdp.Tab, point pointer, options ClickOptions) error {
	if err := ensureVisible(ctx, tab); err != nil {
		return err
	}
	moveCursor(ctx, tab, point.X, point.Y)
	button := options.Button
	if button == "" {
		button = "left"
	}
	count := options.ClickCount
	if count == 0 {
		count = 1
	}
	modifiers := modifierMask(options.Modifiers)
	if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": point.X, "y": point.Y, "button": "none", "modifiers": modifiers,
	}); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": point.X, "y": point.Y, "button": button, "clickCount": i, "modifiers": modifiers,
		}); err != nil {
			return err
		}
		if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": point.X, "y": point.Y, "button": button, "clickCount": i, "modifiers": modifiers,
		}); err != nil {
			return err
		}
	}
	return nil
}

// hover moves the mouse over an element.
func hover(ctx context.Context, tab *cdp.Tab, point pointer) error {
	if err := ensureVisible(ctx, tab); err != nil {
		return err
	}
	moveCursor(ctx, tab, point.X, point.Y)
	_, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": point.X, "y": point.Y, "button": "none",
	})
	return err
}

// drag drags one element onto another with mouse input.
func drag(ctx context.Context, tab *cdp.Tab, from, to pointer) error {
	if err := ensureVisible(ctx, tab); err != nil {
		return err
	}
	if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": from.X, "y": from.Y, "button": "none"}); err != nil {
		return err
	}
	if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": from.X, "y": from.Y, "button": "left", "clickCount": 1}); err != nil {
		return err
	}
	steps := 8
	for i := 1; i <= steps; i++ {
		x := from.X + (to.X-from.X)*float64(i)/float64(steps)
		y := from.Y + (to.Y-from.Y)*float64(i)/float64(steps)
		if _, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": "left"}); err != nil {
			return err
		}
	}
	moveCursor(ctx, tab, to.X, to.Y)
	_, err := tab.Send(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": to.X, "y": to.Y, "button": "left", "clickCount": 1})
	return err
}

// moveCursor shows where the agent acts, in a shadow root outside body.
func moveCursor(ctx context.Context, tab *cdp.Tab, x, y float64) {
	_, _ = tab.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf(`(function(x,y){
  const id='__browsermcp_cursor';
  let host=document.getElementById(id);
  if(!host){
    host=document.createElement('browsermcp-cursor');
    host.id=id; host.setAttribute('aria-hidden','true');
    host.style.cssText='position:fixed;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none;transition:transform 300ms ease-out;';
    host.style.transform='translate('+x+'px, '+y+'px)';
    host.attachShadow({mode:'closed'}).innerHTML='<style>.arrow{position:absolute;left:-3px;top:-3px;filter:drop-shadow(0 2px 3px rgba(0,0,0,.45)) drop-shadow(0 0 6px rgba(37,99,235,.9));}.label{position:absolute;left:22px;top:26px;padding:3px 9px;border-radius:999px;background:#2563eb;color:#fff;font:600 12px/16px system-ui,-apple-system,sans-serif;white-space:nowrap;border:1.5px solid #fff;box-shadow:0 2px 6px rgba(0,0,0,.35);}</style><svg class="arrow" width="30" height="36" viewBox="0 0 20 24"><path d="M1 1 L1 19 L6 14.5 L9.5 22 L12.5 20.7 L9 13.3 L15.5 13 Z" fill="#2563eb" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg><div class="label">Browser MCP</div>';
    document.documentElement.appendChild(host);
  }
  host.style.transform='translate('+x+'px, '+y+'px)';
})(%f, %f)`, x, y),
		"returnByValue": true,
	})
}

// --- Text input ---

const fillScript = `function(text){
  const el = this;
  if (el.focus) el.focus();
  if (el.isContentEditable) {
    el.textContent = text;
  } else if (el.tagName === 'TEXTAREA') {
    const desc = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value');
    if (desc && desc.set) desc.set.call(el, text); else el.value = text;
  } else if (el.tagName === 'INPUT') {
    const desc = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value');
    if (desc && desc.set) desc.set.call(el, text); else el.value = text;
  } else {
    el.value = text;
  }
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return el.value === undefined ? text : el.value;
}`

func fillValue(ctx context.Context, tab *cdp.Tab, objectID, text string) error {
	result, err := callFunctionArgs(ctx, tab, objectID, fillScript, []any{text}, true)
	if err != nil {
		return err
	}
	var value string
	if json.Unmarshal(result, &value) == nil && value != text && value != "" {
		// Some fields (number, date) normalize; that is not an error
		_ = value
	}
	return nil
}

func callFunctionArgs(ctx context.Context, tab *cdp.Tab, objectID, declaration string, args []any, returnByValue bool) (json.RawMessage, error) {
	arguments := make([]map[string]any, 0, len(args))
	for _, arg := range args {
		arguments = append(arguments, map[string]any{"value": arg})
	}
	return tab.Send(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            objectID,
		"functionDeclaration": declaration,
		"arguments":           arguments,
		"returnByValue":       returnByValue,
		"awaitPromise":        true,
		"userGesture":         true,
	})
}

// typeText focuses the element and sends text as keys.
func typeText(ctx context.Context, tab *cdp.Tab, objectID, text string, slowly, paste bool) error {
	if _, err := callFunction(ctx, tab, objectID, "function(){ if(this.focus) this.focus(); return true }", true); err != nil {
		return err
	}
	if paste || len([]rune(text)) > TypeKeysMaxChars {
		_, err := tab.Send(ctx, "Input.insertText", map[string]any{"text": text})
		return err
	}
	for _, char := range text {
		if err := dispatchChar(ctx, tab, char); err != nil {
			return err
		}
		if slowly {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func dispatchChar(ctx context.Context, tab *cdp.Tab, char rune) error {
	spec := keyFor(char)
	params := map[string]any{
		"type": "keyDown", "key": spec.key, "code": spec.code,
		"windowsVirtualKeyCode": spec.vk, "nativeVirtualKeyCode": spec.vk,
		"text": string(char), "unmodifiedText": string(char),
	}
	if _, err := tab.Send(ctx, "Input.dispatchKeyEvent", params); err != nil {
		return err
	}
	_, err := tab.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": spec.key, "code": spec.code,
		"windowsVirtualKeyCode": spec.vk, "nativeVirtualKeyCode": spec.vk,
	})
	return err
}

type keyInfo struct {
	key  string
	code string
	vk   int
}

func keyFor(char rune) keyInfo {
	if char >= 'a' && char <= 'z' {
		upper := 'A' + (char - 'a')
		return keyInfo{key: string(char), code: "Key" + string(upper), vk: int(upper)}
	}
	if char >= 'A' && char <= 'Z' {
		return keyInfo{key: string(char), code: "Key" + string(char), vk: int(char)}
	}
	if char >= '0' && char <= '9' {
		return keyInfo{key: string(char), code: "Digit" + string(char), vk: int(char)}
	}
	switch char {
	case ' ':
		return keyInfo{key: " ", code: "Space", vk: 32}
	case '\n':
		return keyInfo{key: "Enter", code: "Enter", vk: 13}
	case '\t':
		return keyInfo{key: "Tab", code: "Tab", vk: 9}
	}
	return keyInfo{key: string(char), code: "", vk: 0}
}

var namedKeys = map[string]keyInfo{
	"Enter":      {key: "Enter", code: "Enter", vk: 13},
	"Tab":        {key: "Tab", code: "Tab", vk: 9},
	"Escape":     {key: "Escape", code: "Escape", vk: 27},
	"Backspace":  {key: "Backspace", code: "Backspace", vk: 8},
	"Delete":     {key: "Delete", code: "Delete", vk: 46},
	"ArrowUp":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
	"ArrowDown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
	"ArrowLeft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
	"ArrowRight": {key: "ArrowRight", code: "ArrowRight", vk: 39},
	"Home":       {key: "Home", code: "Home", vk: 36},
	"End":        {key: "End", code: "End", vk: 35},
	"PageUp":     {key: "PageUp", code: "PageUp", vk: 33},
	"PageDown":   {key: "PageDown", code: "PageDown", vk: 34},
	"Space":      {key: " ", code: "Space", vk: 32},
}

// pressKey presses a key or combination: Enter, Control+a, Meta+Shift+K.
func pressKey(ctx context.Context, tab *cdp.Tab, combo string) error {
	parts := strings.Split(combo, "+")
	modifiers := 0
	for len(parts) > 1 {
		switch strings.ToLower(parts[0]) {
		case "alt":
			modifiers |= 1
		case "control", "ctrl":
			modifiers |= 2
		case "meta", "cmd", "command":
			modifiers |= 4
		case "shift":
			modifiers |= 8
		default:
			return stepErrorf("Unknown modifier %q in %q", parts[0], combo)
		}
		parts = parts[1:]
	}
	name := parts[0]
	spec, ok := namedKeys[name]
	if !ok {
		runes := []rune(name)
		if len(runes) != 1 {
			return stepErrorf("%q isn't a key. Use Enter, Tab, Escape, ArrowDown, Control+a", name)
		}
		spec = keyFor(runes[0])
		if modifiers&8 != 0 && runes[0] >= 'a' && runes[0] <= 'z' {
			spec.key = strings.ToUpper(spec.key)
		}
	}
	text := ""
	if spec.vk == 13 {
		text = "\r"
	} else if spec.vk == 9 {
		text = "\t"
	} else if spec.vk == 32 {
		text = " "
	}
	if _, err := tab.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyDown", "key": spec.key, "code": spec.code,
		"windowsVirtualKeyCode": spec.vk, "nativeVirtualKeyCode": spec.vk,
		"text": text, "modifiers": modifiers,
	}); err != nil {
		return err
	}
	_, err := tab.Send(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": spec.key, "code": spec.code,
		"windowsVirtualKeyCode": spec.vk, "nativeVirtualKeyCode": spec.vk,
		"modifiers": modifiers,
	})
	return err
}

func modifierMask(modifiers []string) int {
	mask := 0
	for _, modifier := range modifiers {
		switch strings.ToLower(modifier) {
		case "alt":
			mask |= 1
		case "control", "controlormeta":
			mask |= 2
		case "meta":
			mask |= 4
		case "shift":
			mask |= 8
		}
	}
	return mask
}

// --- Form controls ---

const selectScript = `function(values){
  if (!this.options) throw new Error('the target is not a <select>');
  const wanted = values.map(value => String(value));
  const matched = [];
  for (const option of this.options) {
    if (wanted.includes(option.value) || wanted.includes(option.label) || wanted.includes(option.text)) {
      option.selected = true; matched.push(option.value);
    }
  }
  if (!matched.length) throw new Error('no option matches ' + wanted.join(', '));
  this.dispatchEvent(new Event('input', { bubbles: true }));
  this.dispatchEvent(new Event('change', { bubbles: true }));
  return matched;
}`

func selectOptions(ctx context.Context, tab *cdp.Tab, objectID string, values []string) ([]string, error) {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	result, err := callFunctionArgs(ctx, tab, objectID, selectScript, []any{args}, true)
	if err != nil {
		return nil, err
	}
	var matched []string
	_ = json.Unmarshal(result, &matched)
	return matched, nil
}

const checkedScript = `function(want){
  if (!!this.checked !== !!want) this.click();
  return !!this.checked;
}`

func setChecked(ctx context.Context, tab *cdp.Tab, objectID string, want bool) error {
	result, err := callFunctionArgs(ctx, tab, objectID, checkedScript, []any{want}, true)
	if err != nil {
		return err
	}
	var now bool
	if json.Unmarshal(result, &now) == nil && now != want {
		return stepErrorf("The control did not change")
	}
	return nil
}

// --- Scrolling ---

const scrollScript = `function(direction, px){
  const doc = document.scrollingElement || document.documentElement;
  const scrollable = el => {
    const style = getComputedStyle(el);
    return /(auto|scroll)/.test(style.overflowY) && el.scrollHeight > el.clientHeight + 1;
  };
  let area = scrollable(doc) ? doc : null;
  if (!area) {
    let best = 0;
    for (const el of document.querySelectorAll('*')) {
      if (!scrollable(el)) continue;
      const size = el.clientWidth * el.clientHeight;
      if (size > best) { best = size; area = el; }
    }
  }
  if (!area) area = doc;
  const max = Math.max(0, area.scrollHeight - area.clientHeight);
  if (direction === 'top') area.scrollTop = 0;
  else if (direction === 'bottom') area.scrollTop = max;
  else if (direction === 'up') area.scrollTop = Math.max(0, area.scrollTop - px);
  else area.scrollTop = Math.min(max, area.scrollTop + px);
  return { top: Math.round(area.scrollTop), max: Math.round(max), atBottom: area.scrollTop >= max - 1 };
}`

type scrollResult struct {
	Top      int  `json:"top"`
	Max      int  `json:"max"`
	AtBottom bool `json:"atBottom"`
}

func scrollPage(ctx context.Context, tab *cdp.Tab, direction string, px int) (scrollResult, error) {
	if px <= 0 {
		px = 0
	}
	var result scrollResult
	err := tab.Eval(ctx, fmt.Sprintf("(%s)(%q, %d)", scrollScript, direction, px), &result)
	if err != nil {
		return result, err
	}
	return result, nil
}

// --- Navigation ---

// waitForLoad waits for the load event, up to timeout, with readyState as a
// fallback for pages restored from the back/forward cache.
func waitForLoad(ctx context.Context, tab *cdp.Tab, timeout time.Duration) bool {
	loaded := make(chan struct{}, 1)
	unsubscribe := tab.On("Page.loadEventFired", func(string, json.RawMessage) {
		select {
		case loaded <- struct{}{}:
		default:
		}
	})
	defer unsubscribe()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ready string
		if err := tab.Eval(ctx, "document.readyState", &ready); err == nil && ready == "complete" {
			return true
		}
		select {
		case <-loaded:
			return true
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
	return false
}

// WaitMode is how much of a navigation `goto` waits for.
type WaitMode int

const (
	// WaitLoad waits for the load event (the default).
	WaitLoad WaitMode = iota
	// WaitDOM waits for DOMContentLoaded.
	WaitDOM
	// WaitCommit returns once the new document is committed; its scripts and
	// resources keep loading in the background.
	WaitCommit
)

// ParseWaitMode reads a `--wait` value.
func ParseWaitMode(value string) (WaitMode, bool) {
	switch value {
	case "load":
		return WaitLoad, true
	case "dom":
		return WaitDOM, true
	case "commit":
		return WaitCommit, true
	}
	return WaitLoad, false
}

// navigate goes to url and waits as mode says. Commit and DOM subscribe before
// sending the navigation, so a fast event is not missed.
func navigate(ctx context.Context, tab *cdp.Tab, url string, mode WaitMode) (bool, error) {
	if mode == WaitLoad {
		if _, err := tab.Send(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
			return false, err
		}
		return waitForLoad(ctx, tab, NavigationLoad), nil
	}
	type navEvent struct {
		frameID string
		commit  bool
		dom     bool
		within  bool
	}
	events := make(chan navEvent, 8)
	unsubscribeCommit := tab.On("Page.frameNavigated", func(_ string, params json.RawMessage) {
		var event struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		}
		_ = json.Unmarshal(params, &event)
		select {
		case events <- navEvent{frameID: event.Frame.ID, commit: true}:
		default:
		}
	})
	defer unsubscribeCommit()
	unsubscribeWithin := tab.On("Page.navigatedWithinDocument", func(string, json.RawMessage) {
		select {
		case events <- navEvent{within: true}:
		default:
		}
	})
	defer unsubscribeWithin()
	unsubscribeDOM := tab.On("Page.domContentEventFired", func(string, json.RawMessage) {
		select {
		case events <- navEvent{dom: true}:
		default:
		}
	})
	defer unsubscribeDOM()
	result, err := tab.Send(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return false, err
	}
	var reply struct {
		FrameID string `json:"frameId"`
	}
	_ = json.Unmarshal(result, &reply)
	deadline := time.Now().Add(NavigationLoad)
	committed := false
	for time.Now().Before(deadline) {
		select {
		case event := <-events:
			if event.within {
				return true, nil
			}
			if event.commit && (reply.FrameID == "" || event.frameID == reply.FrameID) {
				committed = true
				if mode == WaitCommit {
					return true, nil
				}
			}
			if mode == WaitDOM && event.dom {
				return true, nil
			}
		case <-time.After(50 * time.Millisecond):
			// After the commit, readyState covers pages restored from the
			// back/forward cache that never fire DOMContentLoaded.
			if mode == WaitDOM && committed {
				var ready string
				if err := tab.Eval(ctx, "document.readyState", &ready); err == nil && (ready == "interactive" || ready == "complete") {
					return true, nil
				}
			}
		case <-ctx.Done():
			return false, nil
		}
	}
	return false, nil
}

func historyMove(ctx context.Context, tab *cdp.Tab, delta int) error {
	result, err := tab.Send(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return err
	}
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(result, &history); err != nil {
		return err
	}
	index := history.CurrentIndex + delta
	if index < 0 || index >= len(history.Entries) {
		return stepErrorf("There's no %s page in the tab's history", map[int]string{-1: "back", 1: "forward"}[delta])
	}
	if _, err := tab.Send(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}); err != nil {
		return err
	}
	waitForLoad(ctx, tab, NavigationLoad)
	return nil
}

func reload(ctx context.Context, tab *cdp.Tab) error {
	if _, err := tab.Send(ctx, "Page.reload", map[string]any{}); err != nil {
		return err
	}
	waitForLoad(ctx, tab, NavigationLoad)
	return nil
}

// --- Screenshots and files ---

type screenshotOptions struct {
	Full    bool
	Target  *Target
	Scale   float64
	Quality int
}

// screenshot captures a JPEG, longest side at most 1568 px.
func screenshot(ctx context.Context, tab *cdp.Tab, res Resolver, options screenshotOptions) ([]byte, error) {
	if err := ensureVisible(ctx, tab); err != nil {
		return nil, err
	}
	params := map[string]any{"format": "jpeg", "quality": 80, "captureBeyondViewport": options.Full}
	quality := options.Quality
	if quality <= 0 {
		quality = 80
	}
	params["quality"] = quality
	var width, height float64
	if options.Target != nil {
		objectID, err := res.One(ctx, *options.Target, FindTimeout)
		if err != nil {
			return nil, err
		}
		result, err := callFunction(ctx, tab, objectID, `function(){
  const r = this.getBoundingClientRect();
  return { x: r.left + window.scrollX, y: r.top + window.scrollY, width: r.width, height: r.height };
}`, true)
		if err != nil {
			return nil, err
		}
		var box struct {
			X, Y, Width, Height float64
		}
		if err := json.Unmarshal(result, &box); err != nil {
			return nil, err
		}
		if box.Width == 0 || box.Height == 0 {
			return nil, stepErrorf("The element has no visible box")
		}
		width, height = box.Width, box.Height
		params["clip"] = map[string]any{"x": box.X, "y": box.Y, "width": box.Width, "height": box.Height, "scale": 1.0}
	} else if options.Full {
		metrics, err := tab.Send(ctx, "Page.getLayoutMetrics", nil)
		if err != nil {
			return nil, err
		}
		var layout struct {
			CSSContentSize struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"cssContentSize"`
		}
		if err := json.Unmarshal(metrics, &layout); err != nil {
			return nil, err
		}
		width, height = layout.CSSContentSize.Width, layout.CSSContentSize.Height
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": width, "height": height, "scale": 1.0}
	} else {
		var size struct {
			Width, Height float64
		}
		_ = tab.Eval(ctx, "({width: innerWidth, height: innerHeight})", &size)
		width, height = size.Width, size.Height
	}
	if scale := options.Scale; scale > 0 {
		if clip, ok := params["clip"].(map[string]any); ok {
			clip["scale"] = scale
		}
	} else if width > 0 && height > 0 {
		scale := 1.0
		longest := width
		if height > longest {
			longest = height
		}
		if longest > ScreenMaxSide {
			scale = ScreenMaxSide / longest
		}
		if clip, ok := params["clip"].(map[string]any); ok {
			clip["scale"] = scale
		} else if scale < 1 {
			params["clip"] = map[string]any{"x": 0, "y": 0, "width": width, "height": height, "scale": scale}
		}
	}
	result, err := tab.Send(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(reply.Data)
}

// printToPDF prints the page as Chrome's Save as PDF does.
func printToPDF(ctx context.Context, tab *cdp.Tab, landscape bool) ([]byte, error) {
	result, err := tab.Send(ctx, "Page.printToPDF", map[string]any{
		"landscape":         landscape,
		"printBackground":   true,
		"preferCSSPageSize": true,
	})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(reply.Data)
}

type fetchedFile struct {
	Base64 string `json:"base64"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int    `json:"size"`
	Type   string `json:"type"`
}

const fetchScript = `async function(url, maxBytes){
  const response = await fetch(url, { credentials: 'include' });
  if (!response.ok) throw new Error('HTTP ' + response.status + ' for ' + response.url);
  const buffer = await response.arrayBuffer();
  if (buffer.byteLength > maxBytes) throw new Error('the file is larger than ' + maxBytes + ' bytes');
  const bytes = new Uint8Array(buffer);
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  let name = '';
  const disposition = response.headers.get('content-disposition') || '';
  const match = /filename\*?=(?:UTF-8''|")?([^";]+)"?/i.exec(disposition);
  if (match) {
    try { name = decodeURIComponent(match[1].trim()); } catch (error) { name = match[1].trim(); }
  }
  if (!name) {
    try { name = new URL(response.url).pathname.split('/').filter(Boolean).pop() || 'download'; } catch (error) { name = 'download'; }
  }
  return { base64: btoa(binary), name, url: response.url, size: bytes.length, type: response.headers.get('content-type') || '' };
}`

// fetchFile fetches a URL inside the page, so it uses the page's cookies and
// CORS rules.
func fetchFile(ctx context.Context, tab *cdp.Tab, url string) (fetchedFile, error) {
	result, err := tab.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    fmt.Sprintf("(%s)(%s, %d)", strings.TrimSuffix(fetchScript, ""), strconv.Quote(url), DownloadMaxBytes),
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	if err != nil {
		return fetchedFile{}, err
	}
	var reply struct {
		Result struct {
			Value fetchedFile `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return fetchedFile{}, err
	}
	if reply.ExceptionDetails != nil {
		return fetchedFile{}, stepErrorf("%s", firstLineOf(reply.ExceptionDetails.Exception.Description))
	}
	return reply.Result.Value, nil
}

// elementRect returns an element's page coordinates.
func elementRect(ctx context.Context, tab *cdp.Tab, objectID string) (map[string]float64, error) {
	result, err := callFunction(ctx, tab, objectID, `function(){
  const r = this.getBoundingClientRect();
  return { x: r.left + window.scrollX, y: r.top + window.scrollY, width: r.width, height: r.height };
}`, true)
	if err != nil {
		return nil, err
	}
	var box map[string]float64
	if err := json.Unmarshal(result, &box); err != nil {
		return nil, err
	}
	return box, nil
}

// scrollIntoView brings an element into view.
func scrollIntoView(ctx context.Context, tab *cdp.Tab, objectID string) error {
	_, err := callFunction(ctx, tab, objectID, `function(){ this.scrollIntoView({block:'center', inline:'center', behavior:'instant'}); return true }`, true)
	return err
}
