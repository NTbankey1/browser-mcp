package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
)

// Target is a snapshot ref, a locator expression or a CSS selector.
type Target struct {
	Kind     string // "ref", "locator" or "css"
	Ref      string
	Selector string
	Calls    []LocatorCall
	Source   string
}

// LocatorCall is one parsed locator method call.
type LocatorCall struct {
	Method string
	Args   []any
}

var refPattern = regexp.MustCompile(`^(f\d+)?e\d+$`)

// ParseTarget classifies a target. A malformed locator expression is an
// error the model sees, not a CSS selector.
func ParseTarget(source string) (Target, error) {
	if refPattern.MatchString(source) {
		return Target{Kind: "ref", Ref: source, Source: source}, nil
	}
	if regexp.MustCompile(`^(page\.)?[a-zA-Z]+\(`).MatchString(source) {
		calls, err := ParseLocatorExpression(source)
		if err != nil {
			return Target{}, err
		}
		return Target{Kind: "locator", Calls: calls, Source: source}, nil
	}
	return Target{Kind: "css", Selector: source, Source: source}, nil
}

type locatorSpec struct {
	maxArgs int
	options []string
}

var locatorMethods = map[string]locatorSpec{
	"getByRole":        {maxArgs: 2, options: []string{"name", "exact", "checked", "disabled", "expanded", "includeHidden", "level", "pressed", "selected"}},
	"getByText":        {maxArgs: 2, options: []string{"exact"}},
	"getByLabel":       {maxArgs: 2, options: []string{"exact"}},
	"getByPlaceholder": {maxArgs: 2, options: []string{"exact"}},
	"getByAltText":     {maxArgs: 2, options: []string{"exact"}},
	"getByTitle":       {maxArgs: 2, options: []string{"exact"}},
	"getByTestId":      {maxArgs: 1},
	"locator":          {maxArgs: 2, options: []string{"hasText", "hasNotText"}},
	"frameLocator":     {maxArgs: 1},
	"contentFrame":     {maxArgs: 0},
	"filter":           {maxArgs: 1, options: []string{"hasText", "hasNotText", "visible"}},
	"first":            {maxArgs: 0},
	"last":             {maxArgs: 0},
	"nth":              {maxArgs: 1},
}

// regexLiteral is a /pattern/flags argument.
type regexLiteral struct {
	Source string
	Flags  string
}

type locatorParser struct {
	text string
	i    int
}

// ParseLocatorExpression parses a locator expression, returning the error the
// model should see.
func ParseLocatorExpression(text string) ([]LocatorCall, error) {
	if strings.HasPrefix(text, "page.") {
		text = text[5:]
	}
	return (&locatorParser{text: text}).parse()
}

func (p *locatorParser) fail(message string) error {
	at := p.text[p.i:]
	if len(at) > 15 {
		at = at[:15]
	}
	return parseErrorf("Can't parse the locator %s at %q: %s. Use getByRole('button', { name: 'Submit' }), getByLabel('Email'), getByText('…'), getByPlaceholder('…'), getByTestId('…'), a ref or a CSS selector", p.text, at, message)
}

func (p *locatorParser) skipSpace() {
	for p.i < len(p.text) && (p.text[p.i] == ' ' || p.text[p.i] == '\t') {
		p.i++
	}
}

func (p *locatorParser) peek() byte {
	p.skipSpace()
	if p.i >= len(p.text) {
		return 0
	}
	return p.text[p.i]
}

func (p *locatorParser) expect(ch byte) error {
	if p.peek() != ch {
		return p.fail("expected " + string(ch))
	}
	p.i++
	return nil
}

func (p *locatorParser) parse() ([]LocatorCall, error) {
	var calls []LocatorCall
	for {
		p.skipSpace()
		start := p.i
		for p.i < len(p.text) && isLetter(p.text[p.i]) {
			p.i++
		}
		if p.i == start {
			return nil, p.fail("expected a method name")
		}
		name := p.text[start:p.i]
		spec, ok := locatorMethods[name]
		if !ok {
			return nil, p.fail(name + "() isn't supported")
		}
		if err := p.expect('('); err != nil {
			return nil, err
		}
		var args []any
		for p.peek() != ')' {
			if len(args) > 0 {
				if err := p.expect(','); err != nil {
					return nil, err
				}
				if p.peek() == ')' {
					break
				}
			}
			value, err := p.parseLiteral()
			if err != nil {
				return nil, err
			}
			args = append(args, value)
		}
		if err := p.expect(')'); err != nil {
			return nil, err
		}
		if len(args) > spec.maxArgs {
			return nil, p.fail(fmt.Sprintf("%s() takes at most %d arguments", name, spec.maxArgs))
		}
		for _, arg := range args {
			object, ok := arg.(map[string]any)
			if !ok {
				continue
			}
			for key := range object {
				allowed := false
				for _, option := range spec.options {
					if option == key {
						allowed = true
					}
				}
				if !allowed {
					return nil, p.fail(fmt.Sprintf("%s() has no %s option", name, key))
				}
			}
		}
		calls = append(calls, LocatorCall{Method: name, Args: args})
		if p.peek() == 0 {
			return calls, nil
		}
		if err := p.expect('.'); err != nil {
			return nil, err
		}
	}
}

func (p *locatorParser) parseLiteral() (any, error) {
	ch := p.peek()
	switch ch {
	case '\'', '"', '`':
		return p.parseString(ch)
	case '/':
		start := p.i
		p.i++
		for p.i < len(p.text) && p.text[p.i] != '/' {
			if p.text[p.i] == '\\' {
				p.i += 2
			} else {
				p.i++
			}
		}
		body := p.text[start+1 : p.i]
		p.i++
		flagsStart := p.i
		for p.i < len(p.text) && strings.ContainsRune("dgimsuy", rune(p.text[p.i])) {
			p.i++
		}
		return regexLiteral{Source: body, Flags: p.text[flagsStart:p.i]}, nil
	case '{':
		p.i++
		object := map[string]any{}
		for p.peek() != '}' {
			if len(object) > 0 {
				if err := p.expect(','); err != nil {
					return nil, err
				}
				if p.peek() == '}' {
					break
				}
			}
			quote := p.peek()
			var key string
			var err error
			if quote == '\'' || quote == '"' {
				key, err = p.parseString(quote)
				if err != nil {
					return nil, err
				}
			} else {
				start := p.i
				for p.i < len(p.text) && isLetter(p.text[p.i]) {
					p.i++
				}
				key = p.text[start:p.i]
				if key == "" {
					return nil, p.fail("expected an option name")
				}
			}
			if err := p.expect(':'); err != nil {
				return nil, err
			}
			value, err := p.parseLiteral()
			if err != nil {
				return nil, err
			}
			if _, nested := value.(map[string]any); nested {
				return nil, p.fail("nested objects aren't supported")
			}
			object[key] = value
		}
		p.i++
		return object, nil
	}
	start := p.i
	if p.i < len(p.text) && isLetter(p.text[p.i]) {
		for p.i < len(p.text) && isLetter(p.text[p.i]) {
			p.i++
		}
		switch p.text[start:p.i] {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, p.fail("expected a string, number, boolean, regex or { … }")
	}
	if p.i < len(p.text) && p.text[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.text) && (p.text[p.i] >= '0' && p.text[p.i] <= '9' || p.text[p.i] == '.') {
		p.i++
	}
	word := p.text[start:p.i]
	if number, err := strconv.ParseFloat(word, 64); err == nil {
		return number, nil
	}
	return nil, p.fail("expected a string, number, boolean, regex or { … }")
}

func (p *locatorParser) parseString(quote byte) (string, error) {
	start := p.i
	p.i++
	var out strings.Builder
	for p.i < len(p.text) && p.text[p.i] != quote {
		if p.text[p.i] == '\\' && p.i+1 < len(p.text) {
			switch p.text[p.i+1] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			default:
				out.WriteByte(p.text[p.i+1])
			}
			p.i += 2
			continue
		}
		out.WriteByte(p.text[p.i])
		p.i++
	}
	if p.i >= len(p.text) {
		p.i = start
		return "", p.fail("missing closing quote")
	}
	p.i++
	return out.String(), nil
}

func isLetter(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// --- Resolution ---

// Resolver resolves targets on one tab against its current ref table. A
// released and re-claimed tab gets a new table, so an old ref fails instead of
// acting on a different element.
type Resolver struct {
	Tab  *cdp.Tab
	Refs func(string) (int, bool)
}

// One resolves a target to exactly one element, waiting for a locator to
// match. It returns the element's remote object ID.
func (r Resolver) One(ctx context.Context, target Target, timeout time.Duration) (string, error) {
	tab := r.Tab
	if target.Kind == "ref" {
		backendID, ok := 0, false
		if r.Refs != nil {
			backendID, ok = r.Refs(target.Ref)
		}
		if !ok {
			return "", stepErrorf("Ref %s isn't on the page anymore. Run `snapshot -i` or use a locator", target.Ref)
		}
		objectID, err := resolveBackendNode(ctx, tab, backendID)
		if err != nil {
			return "", stepErrorf("Ref %s isn't on the page anymore. Run `snapshot -i` or use a locator", target.Ref)
		}
		return objectID, nil
	}
	arrayID, err := r.Many(ctx, target)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	for {
		count, err := countObjects(ctx, tab, arrayID)
		if err != nil {
			return "", err
		}
		if count == 1 {
			return firstObject(ctx, tab, arrayID)
		}
		if count > 1 {
			return "", stepErrorf("%s matched %d elements. Use a ref from `snapshot -i`, add { exact: true }, or pick one with .first() or .nth(i)", target.Source, count)
		}
		if time.Now().After(deadline) {
			return "", stepErrorf("Nothing matches %s. Run `find \"<text>\"` or `snapshot -i` to see what's on the page", target.Source)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// Count resolves a locator to its match count, without waiting.
func (r Resolver) Count(ctx context.Context, target Target) (int, error) {
	tab := r.Tab
	if target.Kind == "ref" {
		if _, err := r.One(ctx, target, 0); err != nil {
			return 0, err
		}
		return 1, nil
	}
	arrayID, err := r.Many(ctx, target)
	if err != nil {
		return 0, err
	}
	return countObjects(ctx, tab, arrayID)
}

// Many evaluates a CSS selector or locator expression into an array of
// elements and returns its remote object ID.
func (r Resolver) Many(ctx context.Context, target Target) (string, error) {
	tab := r.Tab
	var expression string
	if target.Kind == "css" {
		selector, _ := json.Marshal(target.Selector)
		expression = fmt.Sprintf("(function(){try{return Array.from(document.querySelectorAll(%s))}catch(e){throw new Error('not a valid selector: '+e.message)}})()", selector)
	} else {
		calls, err := json.Marshal(serializeCalls(target.Calls))
		if err != nil {
			return "", err
		}
		expression = strings.Replace(resolverScript, "CALLS", string(calls), 1)
	}
	objectID, err := evaluateObject(ctx, tab, expression)
	if err != nil {
		return "", stepErrorf("%s isn't a valid target: %s. Use a ref, a locator such as getByRole('button', { name: 'Submit' }), or a CSS selector", target.Source, ErrorMessage(err))
	}
	if objectID == "" {
		return "", stepErrorf("Nothing matches %s. Run `find \"<text>\"` or `snapshot -i` to see what's on the page", target.Source)
	}
	return objectID, nil
}

func serializeCalls(calls []LocatorCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		args := make([]any, 0, len(call.Args))
		for _, arg := range call.Args {
			args = append(args, serializeLiteral(arg))
		}
		out = append(out, map[string]any{"m": call.Method, "a": args})
	}
	return out
}

func serializeLiteral(value any) any {
	switch typed := value.(type) {
	case regexLiteral:
		return map[string]any{"$regex": typed.Source, "$flags": typed.Flags}
	case map[string]any:
		object := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			object[key] = serializeLiteral(typed[key])
		}
		return object
	default:
		return value
	}
}

// --- CDP helpers ---

func evaluateObject(ctx context.Context, tab *cdp.Tab, expression string) (string, error) {
	result, err := tab.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": false,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	if err != nil {
		return "", err
	}
	return objectIDFromResult(result)
}

func objectIDFromResult(result json.RawMessage) (string, error) {
	var reply struct {
		Result struct {
			ObjectID string `json:"objectId"`
			Subtype  string `json:"subtype"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return "", err
	}
	if reply.ExceptionDetails != nil {
		message := reply.ExceptionDetails.Exception.Description
		if message == "" {
			message = reply.ExceptionDetails.Text
		}
		return "", fmt.Errorf("%s", firstLineOf(message))
	}
	return reply.Result.ObjectID, nil
}

func callFunction(ctx context.Context, tab *cdp.Tab, objectID, declaration string, returnByValue bool) (json.RawMessage, error) {
	return tab.Send(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            objectID,
		"functionDeclaration": declaration,
		"returnByValue":       returnByValue,
		"awaitPromise":        true,
		"userGesture":         true,
	})
}

func countObjects(ctx context.Context, tab *cdp.Tab, arrayID string) (int, error) {
	result, err := callFunction(ctx, tab, arrayID, "function(){return this ? this.length : 0}", true)
	if err != nil {
		return 0, err
	}
	var reply struct {
		Result struct {
			Value int `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return 0, err
	}
	return reply.Result.Value, nil
}

func firstObject(ctx context.Context, tab *cdp.Tab, arrayID string) (string, error) {
	result, err := callFunction(ctx, tab, arrayID, "function(){return (this && this[0]) || null}", false)
	if err != nil {
		return "", err
	}
	return objectIDFromResult(result)
}

func resolveBackendNode(ctx context.Context, tab *cdp.Tab, backendNodeID int) (string, error) {
	result, err := tab.Send(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": backendNodeID})
	if err != nil {
		return "", err
	}
	var reply struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return "", err
	}
	if reply.Object.ObjectID == "" {
		return "", fmt.Errorf("node %d is gone", backendNodeID)
	}
	return reply.Object.ObjectID, nil
}

// resolverScript evaluates a parsed locator. It is the native equivalent of
// Playwright's locator engine for the whitelisted methods: documented
// approximations are in docs/migration/COMPATIBILITY_MATRIX.md.
const resolverScript = `(function(calls){
  const decode = v => (v && typeof v === 'object' && Object.prototype.hasOwnProperty.call(v,'$regex'))
    ? new RegExp(v.$regex, v.$flags || '') : v;
  const dec = a => (a || []).map(decode);
  const norm = s => (s == null ? '' : String(s)).replace(/\s+/g, ' ').trim();
  const textOf = el => norm(el.innerText || el.textContent || '');
  const labelOf = el => (el.labels && el.labels.length)
    ? norm(Array.from(el.labels).map(l => l.innerText || l.textContent).join(' ')) : '';
  const nameOf = el => {
    const doc = el.ownerDocument;
    const labelledby = (el.getAttribute('aria-labelledby') || '').split(/\s+/)
      .map(id => (doc.getElementById(id) || {}).textContent || '').join(' ');
    const own = el.getAttribute('aria-label') || labelledby || labelOf(el) ||
      el.getAttribute('alt') || el.getAttribute('title') || el.getAttribute('placeholder');
    if (own) return norm(own);
    const tag = el.tagName.toLowerCase();
    if (['button','a','summary','option','label','h1','h2','h3','h4','h5','h6'].includes(tag)) return textOf(el);
    return '';
  };
  const implicit = el => {
    const tag = el.tagName.toLowerCase();
    const type = (el.getAttribute('type') || 'text').toLowerCase();
    if (tag === 'button') return 'button';
    if (tag === 'a' && el.hasAttribute('href')) return 'link';
    if (tag === 'input') {
      if (type === 'checkbox') return 'checkbox';
      if (type === 'radio') return 'radio';
      if (['submit','button','reset','image'].includes(type)) return 'button';
      if (type === 'search') return 'searchbox';
      if (type === 'range') return 'slider';
      if (type === 'number') return 'spinbutton';
      if (type === 'hidden') return '';
      return 'textbox';
    }
    if (tag === 'textarea') return 'textbox';
    if (tag === 'select') return el.multiple ? 'listbox' : 'combobox';
    if (tag === 'img') return 'img';
    if (/^h[1-6]$/.test(tag)) return 'heading';
    if (tag === 'ul' || tag === 'ol') return 'list';
    if (tag === 'li') return 'listitem';
    if (tag === 'nav') return 'navigation';
    if (tag === 'main') return 'main';
    if (tag === 'header') return 'banner';
    if (tag === 'footer') return 'contentinfo';
    if (tag === 'form') return 'form';
    if (tag === 'table') return 'table';
    if (tag === 'tr') return 'row';
    if (tag === 'td') return 'cell';
    if (tag === 'th') return 'columnheader';
    if (tag === 'dialog') return 'dialog';
    return '';
  };
  const roleOf = el => (el.getAttribute('role') || '').trim().toLowerCase() || implicit(el);
  const matchText = (value, want, exact) => {
    if (want instanceof RegExp) return want.test(value);
    const w = norm(want);
    return exact ? value === w : value.toLowerCase().includes(w.toLowerCase());
  };
  const isVisible = el => {
    const box = el.getBoundingClientRect();
    const style = el.ownerDocument.defaultView.getComputedStyle(el);
    return box.width > 0 && box.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
  };
  let scopes = [document];
  let els = null;
  const base = () => {
    if (els) return els;
    const out = [];
    for (const scope of scopes) {
      if (!scope || !scope.querySelectorAll) continue;
      for (const el of scope.querySelectorAll('*')) out.push(el);
    }
    return out;
  };
  const opts = o => (o && typeof o === 'object' ? o : {});
  for (const call of calls) {
    const m = call.m, a = dec(call.a);
    switch (m) {
      case 'getByRole': {
        const role = norm(a[0]).toLowerCase();
        const o = opts(a[1]);
        els = base().filter(el => {
          if (roleOf(el) !== role) return false;
          if (o.name !== undefined) {
            const name = nameOf(el) || (el.value || '');
            const matches = o.name instanceof RegExp ? o.name.test(name) : (o.exact ? name === norm(o.name) : name.toLowerCase().includes(norm(o.name).toLowerCase()));
            if (!matches) return false;
          }
          if (o.level !== undefined) {
            const tag = el.tagName.toLowerCase();
            const level = /^h[1-6]$/.test(tag) ? Number(tag[1]) : Number(el.getAttribute('aria-level'));
            if (level !== Number(o.level)) return false;
          }
          if (o.checked !== undefined && !!el.checked !== !!o.checked) return false;
          if (o.disabled !== undefined && (!!el.disabled || el.getAttribute('aria-disabled') === 'true') !== !!o.disabled) return false;
          if (o.selected !== undefined && !!el.selected !== !!o.selected) return false;
          if (o.expanded !== undefined && (el.getAttribute('aria-expanded') === 'true') !== !!o.expanded) return false;
          if (o.pressed !== undefined && (el.getAttribute('aria-pressed') === 'true') !== !!o.pressed) return false;
          return true;
        });
        break;
      }
      case 'getByText': {
        const o = opts(a[1]);
        els = base().filter(el => matchText(textOf(el), a[0], o.exact));
        break;
      }
      case 'getByLabel': {
        const o = opts(a[1]);
        els = base().filter(el => matchText(labelOf(el) || (el.getAttribute('aria-label') || ''), a[0], o.exact));
        break;
      }
      case 'getByPlaceholder': {
        const o = opts(a[1]);
        els = base().filter(el => matchText(el.getAttribute('placeholder') || '', a[0], o.exact));
        break;
      }
      case 'getByAltText': {
        const o = opts(a[1]);
        els = base().filter(el => matchText(el.getAttribute('alt') || '', a[0], o.exact));
        break;
      }
      case 'getByTitle': {
        const o = opts(a[1]);
        els = base().filter(el => matchText(el.getAttribute('title') || '', a[0], o.exact));
        break;
      }
      case 'getByTestId': {
        const want = norm(a[0]);
        els = base().filter(el => el.getAttribute('data-testid') === want || el.getAttribute('data-test-id') === want || el.getAttribute('data-test') === want);
        break;
      }
      case 'locator': {
        const selector = String(a[0]);
        const o = opts(a[1]);
        const roots = els || scopes;
        const out = [];
        for (const root of roots) {
          if (!root || !root.querySelectorAll) continue;
          try { for (const el of root.querySelectorAll(selector)) out.push(el); }
          catch (error) { throw new Error('not a valid selector: ' + error.message); }
        }
        els = out;
        if (o.hasText !== undefined) els = els.filter(el => matchText(textOf(el), o.hasText, false));
        if (o.hasNotText !== undefined) els = els.filter(el => !matchText(textOf(el), o.hasNotText, false));
        break;
      }
      case 'filter': {
        const o = opts(a[0]);
        if (o.hasText !== undefined) els = (els || []).filter(el => matchText(textOf(el), o.hasText, false));
        if (o.hasNotText !== undefined) els = (els || []).filter(el => !matchText(textOf(el), o.hasNotText, false));
        if (o.visible !== undefined) els = (els || []).filter(el => isVisible(el) === !!o.visible);
        break;
      }
      case 'first': els = base().slice(0, 1); break;
      case 'last': { const all = base(); els = all.length ? [all[all.length - 1]] : []; break; }
      case 'nth': { const all = base(); const i = Number(a[0]); els = i < 0 ? all.slice(Math.max(0, all.length + i), all.length + i + 1) : all.slice(i, i + 1); break; }
      case 'frameLocator': {
        const selector = String(a[0]);
        const roots = els || scopes;
        const frames = [];
        for (const root of roots) {
          if (!root || !root.querySelectorAll) continue;
          for (const frame of root.querySelectorAll(selector)) frames.push(frame);
        }
        scopes = frames.map(frame => { try { return frame.contentDocument; } catch (error) { return null; } }).filter(Boolean);
        els = null;
        break;
      }
      case 'contentFrame': {
        const frames = base().filter(el => el.tagName === 'IFRAME');
        scopes = frames.map(frame => { try { return frame.contentDocument; } catch (error) { return null; } }).filter(Boolean);
        els = null;
        break;
      }
      default:
        throw new Error(m + '() isn\'t supported');
    }
  }
  return els || [];
})(CALLS)`
