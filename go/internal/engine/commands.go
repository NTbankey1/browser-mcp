package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/brand"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
)

// CommandDef is one `browser` command.
type CommandDef struct {
	Name        string
	Usage       string
	Help        string
	Flags       FlagSpec
	MaxArgs     int // -1 for no limit
	NoPage      bool
	Reads       bool
	Navigates   bool
	WhileDialog bool
	Capability  string
	Timeout     func(ParsedArgs) time.Duration
	Echo        func(ParsedArgs) string
	Run         func(context.Context, *stepContext) error
}

var commands []*CommandDef
var commandsByName = map[string]*CommandDef{}

func init() {
	commands = []*CommandDef{
		{
			Name:      "goto",
			Usage:     "goto <url> [--wait commit|dom|load]   # the first goto opens a background tab of your own",
			Help:      "Opens a URL in the current tab (the first time, in a new background tab of your own). Waits for the load event (up to 10 s); `--wait dom` returns at DOMContentLoaded and `--wait commit` as soon as the new document is committed, with its resources still loading.",
			Navigates: true,
			NoPage:    true,
			Flags:     FlagSpec{"wait": true},
			MaxArgs:   1,
			Timeout:   func(ParsedArgs) time.Duration { return 45 * time.Second },
			Run:       runGoto,
		},
		{
			Name: "back", Usage: "back | forward | reload", MaxArgs: 0,
			Help: "Goes back in the tab's history, waiting for the load event (up to 10 s).",
			Run:  func(ctx context.Context, c *stepContext) error { return runHistory(ctx, c, -1) },
		},
		{
			Name: "forward", MaxArgs: 0,
			Help: "Goes forward in the tab's history, waiting for the load event (up to 10 s).",
			Run:  func(ctx context.Context, c *stepContext) error { return runHistory(ctx, c, 1) },
		},
		{
			Name: "reload", MaxArgs: 0, Navigates: true,
			Help: "Reloads the page, waiting for the load event (up to 10 s).",
			Run:  runReload,
		},
		{
			Name:  "click",
			Usage: "click <target> [--double] [--button right|middle] [--modifiers Shift,Control]",
			Help:  "Clicks the element's center after scrolling it into view. Fails if it's hidden, disabled or covered (naming what covers it). Waits for a navigation it starts.",
			Flags: FlagSpec{"double": false, "button": true, "modifiers": true}, MaxArgs: 1,
			Run: runClick,
		},
		{
			Name: "hover", Usage: "hover <target>", Help: "Moves the mouse over the element.", MaxArgs: 1,
			Run: runHover,
		},
		{
			Name: "drag", Usage: "drag <source> <target>", Help: "Drags the source element onto the target with mouse input.", MaxArgs: 2,
			Run: runDrag,
		},
		{
			Name:  "scroll",
			Usage: "scroll down|up [<px>] | scroll top|bottom | scroll <target>   # --in <target>: its scroll area",
			Help:  "Scrolls the page by <px> (most of a screen without it), to its top or bottom, or scrolls the target into view.",
			Flags: FlagSpec{"in": true}, MaxArgs: 2,
			Run: runScroll,
		},
		{
			Name: "fill", Usage: `fill <target> "<text>" [--submit]`,
			Help:  "Replaces the field's value (input, textarea, contenteditable) at once, without key presses. --submit presses Enter after. The text isn't echoed back.",
			Flags: FlagSpec{"submit": false}, MaxArgs: 2,
			Echo: func(args ParsedArgs) string {
				text := "fill"
				if len(args.Positional) >= 2 {
					text += " " + args.Positional[0].Text
				}
				if args.Switches["submit"] {
					text += " --submit"
				}
				return text
			},
			Run: runFill,
		},
		{
			Name: "type", Usage: `type "<text>" [--submit] [--slowly] [--paste]   # key by key, into the focused element`,
			Help:  fmt.Sprintf("Types into the focused element key by key. Text over %d characters is inserted at once instead. --slowly waits between keys, --paste inserts the text at once. --submit presses Enter after. The text isn't echoed back.", TypeKeysMaxChars),
			Flags: FlagSpec{"submit": false, "slowly": false, "paste": false}, MaxArgs: 1,
			Echo: func(args ParsedArgs) string {
				text := "type"
				if args.Switches["submit"] {
					text += " --submit"
				}
				if args.Switches["slowly"] {
					text += " --slowly"
				}
				if args.Switches["paste"] {
					text += " --paste"
				}
				return text
			},
			Run: runType,
		},
		{
			Name: "press", Usage: "press <key>   # Enter, Escape, ArrowDown, Control+a",
			Help:    "Presses a key or combination on the focused element. Waits for a navigation it starts.",
			MaxArgs: 1, Run: runPress,
		},
		{
			Name: "select", Usage: `select <target> "<value>"...   # by value or label`,
			Help:    "Selects options of a <select> by value or label (several for a multiple select).",
			MaxArgs: -1, Run: runSelect,
		},
		{
			Name: "check", Usage: "check <target>", Help: "Checks a checkbox, radio or switch (nothing if already checked).",
			MaxArgs: 1, Run: func(ctx context.Context, c *stepContext) error { return runChecked(ctx, c, true) },
		},
		{
			Name: "uncheck", Usage: "uncheck <target>", Help: "Unchecks a checkbox or switch (nothing if already unchecked).",
			MaxArgs: 1, Run: func(ctx context.Context, c *stepContext) error { return runChecked(ctx, c, false) },
		},
		{
			Name: "upload", Usage: "upload <target> <path>...   # local files",
			Help:    "Sets files on a file input. Paths are on your machine: absolute, under ~, or relative to the working directory.",
			MaxArgs: -1,
			Echo: func(args ParsedArgs) string {
				parts := []string{"upload"}
				for _, token := range args.Positional {
					parts = append(parts, token.Text)
				}
				return strings.Join(parts, " ")
			},
			Run: runUpload,
		},
		{
			Name:   "wait",
			Usage:  `wait <ms> | wait --text "<text>" | wait --gone "<text>" | wait --url <glob> | wait <target>`,
			Help:   "Waits a number of ms (up to 60000), or until text appears or disappears, the URL matches (a glob with * and **, or a substring), or the target is visible. Times out after 10 s; change it with --timeout <ms>. --text matches the page's visible text: a placeholder or an attribute needs a target instead. A timeout reports the page's URL, ready state and visible text.",
			Flags:  FlagSpec{"text": true, "gone": true, "url": true, "timeout": true},
			NoPage: true, MaxArgs: 1, Run: runWait,
		},
		{
			Name: "dialog", Usage: `dialog accept ["<text>"] | dialog dismiss   # a dialog ends the batch it opens in`,
			Help:    `Answers the open alert, confirm, prompt or beforeunload dialog. accept "<text>" fills a prompt.`,
			MaxArgs: 2, WhileDialog: true,
			Echo: func(args ParsedArgs) string {
				parts := []string{"dialog"}
				for _, token := range args.Positional {
					parts = append(parts, token.Text)
				}
				return strings.Join(parts, " ")
			},
			Run: runDialog,
		},
		{
			Name: "tab", Usage: "tab list | tab new [<url>] | tab select <tabId> | tab close [<tabId>]   # close: tabs you opened",
			Help:   "Lists the browser's tabs (* marks yours), opens a background tab of your own, switches to a tab (one of the user's too), or closes a tab you opened.",
			NoPage: true, Navigates: true, Flags: FlagSpec{"browser": true}, MaxArgs: 2, WhileDialog: true,
			Timeout: func(ParsedArgs) time.Duration { return 45 * time.Second },
			Run:     runTab,
		},
		{
			Name: "browser", Usage: `browser list | browser use "<name>"|<id>   # the connected browsers (Chrome profiles), and which one new tabs open in`,
			Help:   "Lists the connected browsers (Chrome profiles), * marking the one your new tabs open in, or chooses it.",
			NoPage: true, WhileDialog: true, MaxArgs: 2, Run: runBrowser,
		},
		{
			Name: "snapshot", Usage: "snapshot [<target>] [-i] [--depth N] [--max <chars>|all]   # -i: interactive elements, headings, landmarks",
			Help:  "Prints the accessibility tree with refs (e12) to use as targets. -i keeps interactive elements, landmarks, headings and cells. A target or --depth narrows it; refs stay valid either way. --max <chars> caps the printed characters; all prints everything.",
			Flags: FlagSpec{"i": false, "depth": true, "max": true}, MaxArgs: 1, Reads: true,
			Run: runSnapshot,
		},
		{
			Name: "find", Usage: `find "<text>" [--max <chars>|all]   # snapshot lines containing the text`,
			Help:  "Prints the snapshot lines whose name, text or URL contain the text (ignoring case), each with its ancestors. --max <chars> caps the printed characters.",
			Flags: FlagSpec{"max": true}, MaxArgs: 1, Reads: true, Run: runFind,
		},
		{
			Name: "get", Usage: "get text|value|count [<target>] [--max <chars>|all] | get attr <name> <target> | get url | get title",
			Help:  "text: the target's visible text (the page's without a target). value: a field's value. count: how many elements match. attr: an attribute. --max <chars> caps the printed characters.",
			Flags: FlagSpec{"max": true}, MaxArgs: 3, Reads: true, Run: runGet,
		},
		{
			Name: "screenshot", Usage: "screenshot [<target>] [--full] [--path <file>] [--expect <file>]   # JPEG; --expect compares a baseline",
			Help:  "Adds a JPEG of the viewport (or the element, or the full page) to the result, longest side ≤ 1568 px. --path also saves it. --expect compares it with a baseline file: the first run writes the baseline, later runs fail with [visual-diff] when the image differs.",
			Flags: FlagSpec{"full": false, "path": true, "expect": true}, MaxArgs: 1, Reads: true, Run: runScreenshot,
		},
		{
			Name: "pdf", Usage: "pdf [<path>] [--landscape]   # saves the page as a PDF",
			Help:  "Prints the page to a PDF, as Chrome's Save as PDF does, and saves it to <path> (or --path).",
			Flags: FlagSpec{"landscape": false, "path": true}, MaxArgs: 1,
			Timeout: func(ParsedArgs) time.Duration { return 60 * time.Second }, Run: runPDF,
		},
		{
			Name: "download", Usage: "download <url>|<target> [--path <file>]   # saves a file",
			Help:  fmt.Sprintf("Saves a file: first as the page would fetch it (its cookies, same origin), then, for a URL on another site, with a plain request from this program that carries no cookies, so public files download from any site and a file behind a login does not. Up to %d MB.", DownloadMaxBytes/1024/1024),
			Flags: FlagSpec{"path": true}, MaxArgs: 1,
			Timeout: func(ParsedArgs) time.Duration { return 60 * time.Second }, Run: runDownload,
		},
		{
			Name: "session", Usage: "session save <file> | session restore <file>   # the app's tabs",
			Help:    "Saves the tabs this AI app is using (their URLs and which one was current) to a JSON file, or opens them again in the same browser: `restore` makes the saved current tab current, and refuses a URL the allowlist blocks.",
			MaxArgs: 2, Run: runSession,
		},
		{
			Name: "eval", Usage: `eval "<function>" [<target>]   # runs in the page; e.g. eval "() => location.href"`,
			Help:  "Runs JavaScript in the page and prints the result as JSON: a function (async is fine; with a target it gets the element) or an expression.",
			Flags: FlagSpec{"max": true}, MaxArgs: 2, Run: runEval,
		},
		{
			Name: "console", Usage: "console [--errors]   # with uncaught errors",
			Help:  "Prints the tab's console messages and uncaught errors with their stacks (the last 500 of each), newest last. --errors keeps console errors and uncaught errors only.",
			Flags: FlagSpec{"errors": false, "max": true}, MaxArgs: 0, Reads: true, Run: runConsole,
		},
		{
			Name: "requests", Usage: `requests [--static] [--filter "<text>"]`,
			Help:  "Lists the tab's requests (the last 500): #id, method, status, URL. Images, scripts, styles and fonts only with --static. --filter keeps URLs containing the text.",
			Flags: FlagSpec{"static": false, "filter": true, "max": true}, MaxArgs: 0, Reads: true, Run: runRequests,
		},
		{
			Name: "request", Usage: "request <id> [--body]",
			Help:  "Shows a request's headers, its post data, and the response's headers; --body adds the response body (capped).",
			Flags: FlagSpec{"body": false, "max": true}, MaxArgs: 1, Reads: true, Run: runRequest,
		},
		{
			Name: "cdp", Usage: "cdp <Domain.method> [<json>] | cdp on <Domain.event>... | cdp events [<Domain.event>] [--max N] | cdp wait <Domain.event> [--timeout ms]",
			Help:       "Raw Chrome DevTools Protocol on the tab (only with --caps cdp). `cdp on` buffers events (500), `cdp events` prints and clears them, `cdp wait` waits for the next one.",
			Capability: "cdp", Flags: FlagSpec{"max": true, "timeout": true}, MaxArgs: -1, Run: runCdp,
		},
		{
			Name: "help", Usage: "help [<command>]", Help: "Prints the commands, or one command's details.",
			NoPage: true, WhileDialog: true, MaxArgs: 1, Run: runHelp,
		},
	}
	for _, command := range commands {
		commandsByName[command.Name] = command
	}
	// `back` covers back, forward and reload in the description
	commandsByName["back"].Usage = "back | forward | reload"
}

func positional(args ParsedArgs, index int, what string) (string, error) {
	value, ok := args.Arg(index)
	if !ok {
		return "", stepErrorf("Missing %s", what)
	}
	return value, nil
}

// --- Navigation ---

func runGoto(ctx context.Context, c *stepContext) error {
	raw, err := positional(c.args, 0, "<url>")
	if err != nil {
		return err
	}
	if err := checkAllowed(raw); err != nil {
		return err
	}
	mode := WaitLoad
	if value, ok := c.args.Value("wait"); ok {
		parsed, valid := ParseWaitMode(value)
		if !valid {
			return stepErrorf("--wait is commit, dom or load")
		}
		mode = parsed
	}
	state, err := c.pageForGoto(ctx)
	if err != nil {
		return err
	}
	if loaded, err := navigate(ctx, state.Tab, normalizeURL(raw), mode); err != nil {
		return err
	} else if !loaded {
		c.Note("the page is still loading")
	}
	return nil
}

func runHistory(ctx context.Context, c *stepContext, delta int) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	return historyMove(ctx, state.Tab, delta)
}

func runReload(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	return reload(ctx, state.Tab)
}

// --- Pointer ---

func runClick(ctx context.Context, c *stepContext) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	point, err := preparePointer(ctx, res, target)
	if err != nil {
		return err
	}
	options := ClickOptions{Button: "left", ClickCount: 1}
	if button, ok := c.args.Value("button"); ok {
		if button != "left" && button != "right" && button != "middle" {
			return stepErrorf("--button takes left, right or middle")
		}
		options.Button = button
	}
	if c.args.Switches["double"] {
		options.ClickCount = 2
	}
	if modifiers, ok := c.args.Value("modifiers"); ok {
		options.Modifiers = strings.Split(modifiers, ",")
	}
	timedOut, err := settle(ctx, res.Tab, func() error { return click(ctx, res.Tab, point, options) })
	if err != nil {
		return err
	}
	if timedOut {
		c.Note("the page is still loading")
	}
	return nil
}

func runHover(ctx context.Context, c *stepContext) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	point, err := preparePointer(ctx, res, target)
	if err != nil {
		return err
	}
	return hover(ctx, res.Tab, point)
}

func runDrag(ctx context.Context, c *stepContext) error {
	fromTarget, err := c.target(0)
	if err != nil {
		return err
	}
	toTarget, err := c.target(1)
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	from, err := preparePointer(ctx, res, fromTarget)
	if err != nil {
		return err
	}
	to, err := preparePointer(ctx, res, toTarget)
	if err != nil {
		return err
	}
	return drag(ctx, res.Tab, from, to)
}

func runScroll(ctx context.Context, c *stepContext) error {
	target, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	first, err := positional(c.args, 0, "<direction|target>")
	if err != nil {
		return err
	}
	direction := strings.ToLower(first)
	if direction == "down" || direction == "up" || direction == "top" || direction == "bottom" {
		px := 0
		if value, ok := c.args.Arg(1); ok {
			px, _ = asInt(value)
		}
		result, err := scrollPage(ctx, target.Tab, direction, px)
		if err != nil {
			return err
		}
		if result.AtBottom {
			c.Note("at the bottom")
		} else {
			c.Note(fmt.Sprintf("%d of %d", result.Top, result.Max))
		}
		return nil
	}
	if in, ok := c.args.Value("in"); ok {
		inTarget, err := ParseTarget(in)
		if err != nil {
			return err
		}
		elementTarget, err := ParseTarget(first)
		if err != nil {
			return err
		}
		container, err := target.One(ctx, inTarget, FindTimeout)
		if err != nil {
			return err
		}
		element, err := target.One(ctx, elementTarget, FindTimeout)
		if err != nil {
			return err
		}
		_, err = target.Tab.Send(ctx, "Runtime.callFunctionOn", map[string]any{
			"objectId": container,
			"functionDeclaration": `function(el){
  const box = el.getBoundingClientRect();
  const mine = this.getBoundingClientRect();
  this.scrollTop += (box.top - mine.top) - (this.clientHeight - box.height) / 2;
  return this.scrollTop;
}`,
			"arguments":     []map[string]any{{"objectId": element}},
			"returnByValue": true,
		})
		return err
	}
	parsed, err := ParseTarget(first)
	if err != nil {
		return err
	}
	objectID, err := target.One(ctx, parsed, FindTimeout)
	if err != nil {
		return err
	}
	return scrollIntoView(ctx, target.Tab, objectID)
}

// --- Text and forms ---

func runFill(ctx context.Context, c *stepContext) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	text, err := positional(c.args, 1, "<text>")
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return err
	}
	if err := fillValue(ctx, res.Tab, objectID, text); err != nil {
		return err
	}
	if c.args.Switches["submit"] {
		return pressKey(ctx, res.Tab, "Enter")
	}
	return nil
}

func runType(ctx context.Context, c *stepContext) error {
	text, err := positional(c.args, 0, "<text>")
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	objectID, err := activeElement(ctx, state.Tab)
	if err != nil {
		return err
	}
	if objectID == "" {
		return stepErrorf("Nothing is focused. Click the field first, or use `fill <target> \"<text>\"`")
	}
	if err := typeText(ctx, state.Tab, objectID, text, c.args.Switches["slowly"], c.args.Switches["paste"]); err != nil {
		return err
	}
	if c.args.Switches["submit"] {
		return pressKey(ctx, state.Tab, "Enter")
	}
	return nil
}

func runPress(ctx context.Context, c *stepContext) error {
	key, err := positional(c.args, 0, "<key>")
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	timedOut, err := settle(ctx, state.Tab, func() error { return pressKey(ctx, state.Tab, key) })
	if err != nil {
		return err
	}
	if timedOut {
		c.Note("the page is still loading")
	}
	return nil
}

func runSelect(ctx context.Context, c *stepContext) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	if len(c.args.Positional) < 2 {
		return stepErrorf("Missing <value>")
	}
	values := make([]string, 0, len(c.args.Positional)-1)
	for _, token := range c.args.Positional[1:] {
		values = append(values, token.Text)
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return err
	}
	matched, err := selectOptions(ctx, res.Tab, objectID, values)
	if err != nil {
		return err
	}
	c.Note("selected " + strings.Join(matched, ", "))
	return nil
}

func runChecked(ctx context.Context, c *stepContext, want bool) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return err
	}
	return setChecked(ctx, res.Tab, objectID, want)
}

func runUpload(ctx context.Context, c *stepContext) error {
	target, err := c.target(0)
	if err != nil {
		return err
	}
	if len(c.args.Positional) < 2 {
		return stepErrorf("Missing <path>")
	}
	files := make([]string, 0, len(c.args.Positional)-1)
	var temps []string
	// The page reads an uploaded file when its form is submitted (or when its
	// upload handler fires), not when the input is set, so the staged copies
	// must outlive this step. Delete them later instead of right away.
	defer func() {
		for _, temp := range temps {
			dir := temp
			time.AfterFunc(10*time.Minute, func() { os.RemoveAll(dir) })
		}
	}()
	for _, token := range c.args.Positional[1:] {
		// The thin process reads the file where the AI app runs, so `~` and
		// paths relative to its MCP roots mean what the user expects.
		if c.session.IO != nil {
			name, _, data, err := c.session.IO.ReadFile(ctx, token.Text)
			if err != nil {
				return stepErrorf("%s", ErrorMessage(err))
			}
			dir, err := os.MkdirTemp("", "browsermcp-upload-*")
			if err != nil {
				return err
			}
			temps = append(temps, dir)
			path := filepath.Join(dir, filepath.Base(name))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
			files = append(files, path)
			continue
		}
		path, err := expandPath(token.Text)
		if err != nil {
			return err
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return stepErrorf("There's no file at %s", path)
		}
		files = append(files, path)
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	objectID, err := res.One(ctx, target, FindTimeout)
	if err != nil {
		return err
	}
	_, err = res.Tab.Send(ctx, "DOM.setFileInputFiles", map[string]any{"files": files, "objectId": objectID})
	return err
}

// --- Waiting and dialogs ---

func runWait(ctx context.Context, c *stepContext) error {
	timeout := 10 * time.Second
	if value, ok := c.args.Value("timeout"); ok {
		if ms, ok := asInt(value); ok && ms >= 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	deadline := time.Now().Add(timeout)
	startURL, _ := c.engine.quickURL(ctx, c.engine.currentTab(c.session))
	if text, ok := c.args.Value("text"); ok {
		var last textProbe
		return poll(ctx, deadline, func() bool {
			probe, ok := probeText(ctx, c.engine.tabOf(c.session), text)
			if ok {
				last = probe
			}
			return ok && probe.Found
		}, func() string {
			// The same two failures look alike: a text that isn't there, and a
			// text that is (in another case, or in an attribute such as a
			// placeholder). Say which one the page is.
			var hints []string
			if last.CaseFold && !last.Found {
				hints = append(hints, "it is on the page, in a different case")
			}
			if last.InHTML && !last.Found {
				hints = append(hints, "it is in the page's HTML but not in its visible text: a placeholder, an attribute, or hidden")
			}
			return waitFailure(
				fmt.Sprintf("The text %q didn't appear within %s", text, roundSeconds(timeout)),
				last.pageState(), startURL, hints,
			)
		})
	}
	if text, ok := c.args.Value("gone"); ok {
		var last textProbe
		return poll(ctx, deadline, func() bool {
			probe, ok := probeText(ctx, c.engine.tabOf(c.session), text)
			if ok {
				last = probe
			}
			return ok && !probe.Found
		}, func() string {
			return waitFailure(
				fmt.Sprintf("The text %q is still on the page after %s", text, roundSeconds(timeout)),
				last.pageState(), startURL, nil,
			)
		})
	}
	if pattern, ok := c.args.Value("url"); ok {
		matches := urlMatcher(pattern)
		var last pageState
		return poll(ctx, deadline, func() bool {
			state, ok := readPageState(ctx, c.engine.tabOf(c.session))
			if ok {
				last = state
			}
			return ok && matches(state.URL)
		}, func() string {
			return waitFailure(
				fmt.Sprintf("The URL did not match %q within %s", pattern, roundSeconds(timeout)),
				last, startURL, nil,
			)
		})
	}
	first, err := positional(c.args, 0, "<ms|target>")
	if err != nil {
		return err
	}
	if ms, ok := asInt(first); ok {
		if ms < 0 {
			ms = 0
		}
		if ms > 60_000 {
			ms = 60_000
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	target, err := ParseTarget(first)
	if err != nil {
		return err
	}
	res, _, err := c.resolver(ctx)
	if err != nil {
		return err
	}
	return poll(ctx, deadline, func() bool {
		_, err := res.One(ctx, target, 100*time.Millisecond)
		return err == nil
	}, func() string {
		state, _ := readPageState(ctx, c.engine.tabOf(c.session))
		return waitFailure(
			fmt.Sprintf("Nothing matches %s after %s", first, roundSeconds(timeout)),
			state, startURL, nil,
		)
	})
}

func poll(ctx context.Context, deadline time.Time, check func() bool, failure func() string) error {
	for {
		if check() {
			return nil
		}
		if time.Now().After(deadline) {
			return stepErrorf("%s", failure())
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// pageState is what a timed-out wait needs to tell a slow page from a wrong
// target: where the page is, whether it finished loading, and what it shows.
type pageState struct {
	ReadyState string `json:"readyState"`
	URL        string `json:"url"`
	Sample     string `json:"sample"`
}

// textProbe is one look at a page for `wait --text` or `wait --gone`:
// whether the text is in its visible text, in another case, only in its HTML,
// and the page state a timeout reports.
type textProbe struct {
	Found      bool   `json:"found"`
	CaseFold   bool   `json:"caseFold"`
	InHTML     bool   `json:"inHtml"`
	ReadyState string `json:"readyState"`
	URL        string `json:"url"`
	Sample     string `json:"sample"`
}

func (p textProbe) pageState() pageState {
	return pageState{ReadyState: p.ReadyState, URL: p.URL, Sample: p.Sample}
}

// tabOf is the current tab's state, or nil (no tab, or one the engine
// detached from): the waits poll it without claiming the tab, so a timeout
// still reports what the page shows.
func (e *Engine) tabOf(session *Session) *TabState {
	return e.stateForID(e.currentTab(session))
}

const textProbeExpression = `(() => {
	const body = document.body;
	const text = body ? body.innerText : "";
	const html = document.documentElement ? document.documentElement.outerHTML : "";
	const needle = %s;
	return {
		found: text.includes(needle),
		caseFold: text.toLowerCase().includes(needle.toLowerCase()),
		inHtml: html.includes(needle),
		readyState: document.readyState,
		url: location.href,
		sample: text.replace(/\s+/g, " ").trim().slice(0, 160),
	};
})()`

func probeText(ctx context.Context, state *TabState, text string) (textProbe, bool) {
	if state == nil || state.Tab.Closed() {
		return textProbe{}, false
	}
	var probe textProbe
	if err := state.Tab.Eval(ctx, fmt.Sprintf(textProbeExpression, strconv.Quote(text)), &probe); err != nil {
		return textProbe{}, false
	}
	return probe, true
}

const pageStateExpression = `(() => {
	const body = document.body;
	const text = body ? body.innerText : "";
	return {
		readyState: document.readyState,
		url: location.href,
		sample: text.replace(/\s+/g, " ").trim().slice(0, 160),
	};
})()`

func readPageState(ctx context.Context, state *TabState) (pageState, bool) {
	if state == nil || state.Tab.Closed() {
		return pageState{}, false
	}
	var page pageState
	if err := state.Tab.Eval(ctx, pageStateExpression, &page); err != nil {
		return pageState{}, false
	}
	return page, true
}

// waitFailure is a wait's timeout message: the page's own state, so the model
// can tell a wrong string from a slow load or a navigation, without a second
// call to look.
func waitFailure(base string, state pageState, startURL string, hints []string) string {
	details := append([]string{}, hints...)
	if state.URL != "" && startURL != "" && state.URL != startURL {
		details = append(details, "the page navigated to "+shortURL(state.URL, startURL)+" meanwhile")
	}
	if state.ReadyState != "" && state.ReadyState != "complete" {
		details = append(details, "the page was still loading (readyState "+state.ReadyState+")")
	}
	if state.Sample != "" {
		details = append(details, "the page shows "+quote(Truncate(state.Sample, 160)))
	} else if state.ReadyState == "complete" {
		details = append(details, "the page has no visible text")
	}
	if len(details) == 0 {
		return base
	}
	return base + "; " + strings.Join(details, "; ")
}

func runDialog(ctx context.Context, c *stepContext) error {
	action, err := positional(c.args, 0, "accept|dismiss")
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	if state.Dialog() == nil {
		return stepErrorf("There's no open dialog")
	}
	params := map[string]any{"accept": action == "accept"}
	if action == "accept" {
		if text, ok := c.args.Arg(1); ok {
			params["promptText"] = text
		}
	} else if action != "dismiss" {
		return stepErrorf("dialog takes accept or dismiss")
	}
	_, err = state.Tab.Send(ctx, "Page.handleJavaScriptDialog", params)
	return err
}

// --- Tabs and browsers ---

func runTab(ctx context.Context, c *stepContext) error {
	if len(c.args.Positional) == 0 {
		return stepErrorf("tab takes list, new, select or close")
	}
	action := c.args.Positional[0].Text
	switch action {
	case "list":
		text, err := c.engine.listTabs(ctx, c.session)
		if err != nil {
			return err
		}
		c.Print(text)
		return nil
	case "new":
		// New tabs open in the browser of the tab the session works in, as
		// the tool's `browser use` documents.
		browserID := c.engine.tabBrowser(c.session)
		if label, ok := c.args.Value("browser"); ok {
			browser, err := c.engine.findBrowser(label)
			if err != nil {
				return err
			}
			browserID = browser.ID
		}
		tabID, err := c.engine.browsers.CreateTab(ctx, "", c.session.App, browserID)
		if err != nil {
			return err
		}
		c.engine.adopt(c.session, tabID, true)
		if c.engine.severalBrowsers() {
			if browserID := c.engine.tabBrowser(c.session); browserID != "" {
				for _, summary := range c.engine.browsers.Browsers() {
					if summary.ID == browserID {
						c.Note("in " + summary.Label)
					}
				}
			}
		}
		if url, ok := c.args.Arg(1); ok {
			if err := checkAllowed(url); err != nil {
				return err
			}
			state, err := c.tabState(ctx)
			if err != nil {
				return err
			}
			if loaded, err := navigate(ctx, state.Tab, normalizeURL(url), WaitLoad); err != nil {
				return err
			} else if !loaded {
				c.Note("the page is still loading")
			}
		}
		return nil
	case "select":
		raw, err := positional(c.args, 1, "<tabId>")
		if err != nil {
			return err
		}
		tabID, ok := asInt(raw)
		if !ok {
			return stepErrorf("<tabId> is a number from `tab list`")
		}
		tab, found := c.engine.browsers.FindTab(tabID)
		if !found {
			return stepErrorf("There's no tab %d. Run `tab list`", tabID)
		}
		if (tab.AppID == nil || *tab.AppID != c.session.App.ID) && !tab.Pinned {
			if err := c.engine.browsers.GroupTab(ctx, tabID, &c.session.App); err != nil {
				return err
			}
		}
		c.engine.adopt(c.session, tabID, false)
		return nil
	case "close":
		tabID := c.engine.currentTab(c.session)
		if value, ok := c.args.Arg(1); ok {
			id, ok := asInt(value)
			if !ok {
				return stepErrorf("<tabId> is a number from `tab list`")
			}
			tabID = id
		}
		if tabID == 0 {
			return stepErrorf("There's no current tab")
		}
		c.engine.mu.Lock()
		opened := c.session.opened[tabID]
		c.engine.mu.Unlock()
		if !opened {
			return stepErrorf("Tab %d isn't one you opened, so it stays open. Switch away with `tab select <id>`", tabID)
		}
		c.engine.releaseTab(ctx, tabID)
		if err := c.engine.browsers.CloseTab(ctx, tabID); err != nil {
			return err
		}
		// The extension reports the closed tab a moment later; `tab list` in
		// the same call must not show it
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, found := c.engine.browsers.FindTab(tabID); !found {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		c.engine.mu.Lock()
		for _, session := range c.engine.sessions {
			c.engine.forgetLocked(session, tabID)
		}
		c.engine.mu.Unlock()
		return nil
	default:
		return stepErrorf("tab takes list, new, select or close")
	}
}

func runBrowser(ctx context.Context, c *stepContext) error {
	action, err := positional(c.args, 0, "list|use")
	if err != nil {
		return err
	}
	switch action {
	case "list":
		c.Print(c.engine.listBrowsers(c.session))
		return nil
	case "use":
		name, err := positional(c.args, 1, "<name>")
		if err != nil {
			return err
		}
		browser, err := c.engine.findBrowser(name)
		if err != nil {
			return err
		}
		c.engine.mu.Lock()
		c.session.browser = browser.ID
		c.session.browserChosen = true
		c.engine.mu.Unlock()
		c.Note("new tabs open in " + browser.Label())
		return nil
	default:
		return stepErrorf("browser takes list or use")
	}
}

func (e *Engine) listTabs(ctx context.Context, session *Session) (string, error) {
	tabs := e.browsers.Tabs()
	if len(tabs) == 0 {
		return "(no tabs)", nil
	}
	current := e.currentTab(session)
	labels := map[string]string{}
	if e.severalBrowsers() {
		for _, summary := range e.browsers.Browsers() {
			labels[summary.ID] = summary.Label
		}
	}
	var lines []string
	for _, tab := range tabs {
		url, title := tab.URL, tab.Title
		if state := e.stateForID(tab.TabID); state != nil && !state.Tab.Closed() {
			if live, err := e.quickURL(ctx, tab.TabID); err == nil && live != "" {
				url = live
			}
			if live, err := e.quickTitle(ctx, tab.TabID); err == nil && live != "" {
				title = live
			}
		}
		var marks []string
		e.mu.Lock()
		if session.tabs[tab.TabID] {
			marks = append(marks, "yours")
		}
		e.mu.Unlock()
		if tab.Active {
			marks = append(marks, "active")
		}
		browser := ""
		if labels[tab.Browser] != "" {
			browser = " [" + labels[tab.Browser] + "]"
		}
		mark := " "
		if tab.TabID == current {
			mark = "*"
		}
		line := fmt.Sprintf("%s %d%s %s — %s", mark, tab.TabID, browser, quote(Truncate(title, 80)), Truncate(url, 120))
		if len(marks) > 0 {
			line += " (" + strings.Join(marks, ", ") + ")"
		}
		lines = append(lines, line)
	}
	if len(labels) > 0 {
		target := e.tabBrowser(session)
		suffix := ""
		if target != "" {
			suffix = "; your new tabs open in " + labels[target]
		}
		lines = append(lines, "Several browsers are connected"+suffix+". `browser list`, `browser use \"<name>\"` and `tab new --browser \"<name>\"` choose where")
	}
	return strings.Join(lines, "\n"), nil
}

func (e *Engine) listBrowsers(session *Session) string {
	browsers := e.browsers.Browsers()
	if len(browsers) == 0 {
		return "(no browsers)"
	}
	target := e.tabBrowser(session)
	if target == "" {
		for _, browser := range browsers {
			if browser.Default {
				target = browser.ID
			}
		}
	}
	var lines []string
	for _, browser := range browsers {
		status := ""
		switch {
		case !browser.Connected:
			status = "disconnected"
		case browser.Pairing == "pending":
			status = "waiting for Allow in its popup"
		default:
			status = fmt.Sprintf("%d tab(s)", browser.Tabs)
			if browser.Paused {
				status += ", paused"
			}
		}
		mark := " "
		if browser.ID == target {
			mark = "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s — %s", mark, browser.ID, quote(browser.Label), status))
	}
	return strings.Join(lines, "\n")
}

// --- Reading ---

func runSnapshot(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	nodes, refs, err := takeSnapshot(ctx, state.Tab, "")
	if err != nil {
		return err
	}
	state.SetRefs(refs)
	filtered := nodes
	if c.args.Switches["i"] {
		filtered = InteractiveOnly(filtered)
	}
	if len(c.args.Positional) > 0 {
		source := c.args.Positional[0].Text
		target, err := ParseTarget(source)
		if err != nil {
			return err
		}
		res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
		scoped, err := scopeSnapshot(ctx, res, target, state, filtered)
		if err != nil {
			return err
		}
		filtered = scoped
	}
	depth := 0
	if value, ok := c.args.Value("depth"); ok {
		if parsed, ok := asInt(value); ok && parsed > 0 {
			depth = parsed
		}
	}
	max, err := readCapArg(c.args)
	if err != nil {
		return err
	}
	c.Print(Cap(Render(filtered, depth), max, ""))
	return nil
}

func scopeSnapshot(ctx context.Context, res Resolver, target Target, state *TabState, nodes []*ariaNode) ([]*ariaNode, error) {
	backendID := 0
	if target.Kind == "ref" {
		if id, ok := state.BackendNodeID(target.Ref); ok {
			backendID = id
		}
	} else {
		objectID, err := res.One(ctx, target, FindTimeout)
		if err != nil {
			return nil, err
		}
		result, err := res.Tab.Send(ctx, "DOM.describeNode", map[string]any{"objectId": objectID})
		if err != nil {
			return nil, err
		}
		var reply struct {
			Node struct {
				BackendNodeID int `json:"backendNodeId"`
			} `json:"node"`
		}
		if json.Unmarshal(result, &reply) == nil {
			backendID = reply.Node.BackendNodeID
		}
	}
	if backendID == 0 {
		return nodes, nil
	}
	if found := findByBackend(nodes, backendID); found != nil {
		return []*ariaNode{found}, nil
	}
	return nodes, nil
}

func findByBackend(nodes []*ariaNode, backendID int) *ariaNode {
	for _, node := range nodes {
		if node.backendNodeID == backendID {
			return node
		}
		if found := findByBackend(node.Children, backendID); found != nil {
			return found
		}
	}
	return nil
}

func runFind(ctx context.Context, c *stepContext) error {
	query, err := positional(c.args, 0, `"<text>"`)
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	nodes, refs, err := takeSnapshot(ctx, state.Tab, "")
	if err != nil {
		return err
	}
	state.SetRefs(refs)
	matches := Find(nodes, query)
	if len(matches) == 0 {
		c.Print(fmt.Sprintf("Nothing matches %q. Run `snapshot -i` to see the page", query))
		return nil
	}
	max, err := readCapArg(c.args)
	if err != nil {
		return err
	}
	c.Print(Cap(strings.Join(matches, "\n"), max, ""))
	return nil
}

func runGet(ctx context.Context, c *stepContext) error {
	if len(c.args.Positional) == 0 {
		return stepErrorf("get takes text, value, count, attr, url or title")
	}
	what := c.args.Positional[0].Text
	max, err := readCapArg(c.args)
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
	switch what {
	case "url":
		var href string
		if err := state.Tab.Eval(ctx, "location.href", &href); err != nil {
			return err
		}
		c.Print(href)
		return nil
	case "title":
		var title string
		if err := state.Tab.Eval(ctx, "document.title", &title); err != nil {
			return err
		}
		c.Print(title)
		return nil
	case "count":
		if len(c.args.Positional) < 2 {
			return stepErrorf("get count needs a target")
		}
		target, err := ParseTarget(c.args.Positional[1].Text)
		if err != nil {
			return err
		}
		count, err := res.Count(ctx, target)
		if err != nil {
			return err
		}
		c.Print(strconv.Itoa(count))
		return nil
	case "text":
		if len(c.args.Positional) >= 2 {
			target, err := ParseTarget(c.args.Positional[1].Text)
			if err != nil {
				return err
			}
			objectID, err := res.One(ctx, target, FindTimeout)
			if err != nil {
				return err
			}
			var text string
			if err := evalOnObject(ctx, state.Tab, objectID, "function(){ return this.innerText || this.textContent || '' }", &text); err != nil {
				return err
			}
			c.Print(Cap(text, max, ""))
			return nil
		}
		var text string
		if err := state.Tab.Eval(ctx, "document.body ? document.body.innerText : ''", &text); err != nil {
			return err
		}
		c.Print(Cap(text, max, ""))
		return nil
	case "value":
		if len(c.args.Positional) < 2 {
			return stepErrorf("get value needs a target")
		}
		target, err := ParseTarget(c.args.Positional[1].Text)
		if err != nil {
			return err
		}
		objectID, err := res.One(ctx, target, FindTimeout)
		if err != nil {
			return err
		}
		var value string
		if err := evalOnObject(ctx, state.Tab, objectID, "function(){ return this.value === undefined ? '' : String(this.value) }", &value); err != nil {
			return err
		}
		c.Print(Cap(value, max, ""))
		return nil
	case "attr":
		if len(c.args.Positional) < 3 {
			return stepErrorf("get attr needs a name and a target")
		}
		name := c.args.Positional[1].Text
		target, err := ParseTarget(c.args.Positional[2].Text)
		if err != nil {
			return err
		}
		objectID, err := res.One(ctx, target, FindTimeout)
		if err != nil {
			return err
		}
		var value any
		if err := evalOnObject(ctx, state.Tab, objectID, "function(){ return this.getAttribute("+strconv.Quote(name)+") }", &value); err != nil {
			return err
		}
		if value == nil {
			return stepErrorf("The element has no %s attribute", name)
		}
		c.Print(Cap(fmt.Sprint(value), max, ""))
		return nil
	}
	return stepErrorf("get takes text, value, count, attr, url or title")
}

func runScreenshot(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
	options := screenshotOptions{Full: c.args.Switches["full"]}
	if len(c.args.Positional) > 0 {
		target, err := ParseTarget(c.args.Positional[0].Text)
		if err != nil {
			return err
		}
		options.Target = &target
	}
	data, err := screenshot(ctx, state.Tab, res, options)
	if err != nil {
		return err
	}
	if path, ok := c.args.Value("path"); ok {
		if err := saveFile(ctx, c.session.IO, path, data, false); err != nil {
			return err
		}
		c.Note("saved " + path)
	}
	if expect, ok := c.args.Value("expect"); ok {
		_, _, baseline, readErr := c.session.IO.ReadFile(ctx, expect)
		if readErr != nil {
			if err := saveFile(ctx, c.session.IO, expect, data, false); err != nil {
				return err
			}
			c.Note("baseline written to " + expect)
		} else if !bytes.Equal(baseline, data) {
			return stepErrorf("the screenshot differs from %s (%d bytes against %d); save a new baseline with `screenshot --path %s`", expect, len(data), len(baseline), expect)
		} else {
			c.Note("matches the baseline " + expect)
		}
	}
	c.Image(data, "image/jpeg")
	return nil
}

func runEval(ctx context.Context, c *stepContext) error {
	code, err := positional(c.args, 0, `"<function>"`)
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	var value any
	if len(c.args.Positional) > 1 {
		target, err := ParseTarget(c.args.Positional[1].Text)
		if err != nil {
			return err
		}
		res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
		objectID, err := res.One(ctx, target, FindTimeout)
		if err != nil {
			return err
		}
		declaration := "async function(){ const value = (" + code + "); return typeof value === 'function' ? await value(this) : await value }"
		result, err := callFunction(ctx, state.Tab, objectID, declaration, true)
		if err != nil {
			return err
		}
		value, err = valueFromCall(result)
		if err != nil {
			return err
		}
	} else {
		expression := "(async()=>{ const value = (" + code + "); return typeof value === 'function' ? await value() : await value })()"
		if err := state.Tab.Eval(ctx, expression, &value); err != nil {
			return err
		}
	}
	c.Print(CompactJSON(value))
	return nil
}

func runConsole(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	entries := state.Console(c.args.Switches["errors"])
	if len(entries) == 0 {
		c.Print("(no console messages)")
		return nil
	}
	var lines []string
	for _, entry := range entries {
		line := entry.Type + ": " + entry.Text
		if entry.Location != "" {
			line += " (" + entry.Location + ")"
		}
		lines = append(lines, line)
	}
	max, err := readCapArg(c.args)
	if err != nil {
		return err
	}
	c.Print(CapTail(strings.Join(lines, "\n"), max))
	return nil
}

var staticTypes = map[string]bool{
	"Image": true, "Stylesheet": true, "Font": true, "Media": true,
	"Script": true, "Manifest": true, "TextTrack": true,
}

func runRequests(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	filter, _ := c.args.Value("filter")
	var lines []string
	for _, entry := range state.Requests() {
		if !c.args.Switches["static"] && staticTypes[entry.Type] {
			continue
		}
		if filter != "" && !strings.Contains(entry.URL, filter) {
			continue
		}
		status := "—"
		if entry.Status != 0 {
			status = strconv.Itoa(entry.Status)
		}
		lines = append(lines, fmt.Sprintf("#%d %s %s %s", entry.ID, entry.Method, status, Truncate(entry.URL, 160)))
	}
	if len(lines) == 0 {
		c.Print("(no requests)")
		return nil
	}
	max, err := readCapArg(c.args)
	if err != nil {
		return err
	}
	c.Print(CapTail(strings.Join(lines, "\n"), max))
	return nil
}

func runRequest(ctx context.Context, c *stepContext) error {
	raw, err := positional(c.args, 0, "<id>")
	if err != nil {
		return err
	}
	id, ok := asInt(strings.TrimPrefix(raw, "#"))
	if !ok {
		return stepErrorf("<id> is the #number `requests` prints")
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	entry, found := state.Request(id)
	if !found {
		return stepErrorf("There's no request #%d. Run `requests`", id)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("#%d %s %s", entry.ID, entry.Method, entry.URL))
	if entry.Status != 0 {
		lines = append(lines, fmt.Sprintf("status %d", entry.Status))
	}
	for _, header := range headerLines(entry.Headers) {
		lines = append(lines, "> "+redactHeader(header))
	}
	if entry.PostData != "" {
		lines = append(lines, "", entry.PostData)
	}
	for _, header := range headerLines(entry.RespHead) {
		lines = append(lines, "< "+redactHeader(header))
	}
	if entry.Failure != "" {
		lines = append(lines, "failed: "+entry.Failure)
	}
	if c.args.Switches["body"] {
		result, err := state.Tab.Send(ctx, "Network.getResponseBody", map[string]any{"requestId": entry.RequestID})
		if err == nil {
			var reply struct {
				Body string `json:"body"`
			}
			if json.Unmarshal(result, &reply) == nil {
				max, _ := readCapArg(c.args)
				lines = append(lines, "", Cap(reply.Body, max, ""))
			}
		}
	}
	c.Print(strings.Join(lines, "\n"))
	return nil
}

// --- Raw CDP ---

func runCdp(ctx context.Context, c *stepContext) error {
	if len(c.args.Positional) == 0 {
		return stepErrorf("cdp takes a method, on, events or wait")
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	action := c.args.Positional[0].Text
	switch action {
	case "on":
		if len(c.args.Positional) < 2 {
			return stepErrorf("cdp on needs an event name")
		}
		cdpState := state.CdpState()
		for _, token := range c.args.Positional[1:] {
			event := token.Text
			if cdpState.Subscribed[event] {
				continue
			}
			cdpState.Subscribed[event] = true
			eventName := event
			state.Tab.On(eventName, func(_ string, params json.RawMessage) {
				state.BufferCdp(eventName, params)
			})
		}
		c.Note("listening for " + strings.Join(positionalTexts(c.args.Positional[1:]), ", "))
		return nil
	case "events":
		cdpState := state.CdpState()
		want := ""
		if len(c.args.Positional) > 1 {
			want = c.args.Positional[1].Text
		}
		var lines []string
		for _, entry := range cdpState.Events {
			if want != "" && entry.Method != want {
				continue
			}
			lines = append(lines, entry.Method+" "+string(entry.Params))
		}
		cdpState.Events = nil
		if len(lines) == 0 {
			c.Print("(no events)")
			return nil
		}
		c.Print(strings.Join(lines, "\n"))
		return nil
	case "wait":
		if len(c.args.Positional) < 2 {
			return stepErrorf("cdp wait needs an event name")
		}
		event := c.args.Positional[1].Text
		timeout := 10 * time.Second
		if value, ok := c.args.Value("timeout"); ok {
			if ms, ok := asInt(value); ok && ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}
		arrived := make(chan json.RawMessage, 1)
		unsubscribe := state.Tab.On(event, func(_ string, params json.RawMessage) {
			select {
			case arrived <- params:
			default:
			}
		})
		defer unsubscribe()
		select {
		case params := <-arrived:
			c.Print(string(params))
			return nil
		case <-time.After(timeout):
			return stepErrorf("%s didn't arrive within %s", event, roundSeconds(timeout))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	method := action
	var params json.RawMessage
	if len(c.args.Positional) > 1 {
		raw := c.args.Positional[1].Text
		if !json.Valid([]byte(raw)) {
			return stepErrorf("The second argument is JSON, e.g. {\"expression\":\"1+1\"}")
		}
		params = json.RawMessage(raw)
	}
	result, err := state.Tab.SendJSON(ctx, "", method, params)
	if err != nil {
		return err
	}
	cdpState := state.CdpState()
	if strings.HasSuffix(method, ".enable") {
		cdpState.Enabled[strings.SplitN(method, ".", 2)[0]] = true
	}
	if strings.HasSuffix(method, ".disable") {
		delete(cdpState.Enabled, strings.SplitN(method, ".", 2)[0])
	}
	cdpState.Used[method] = true
	if len(result) == 0 {
		c.Print("{}")
		return nil
	}
	c.Print(string(result))
	return nil
}

// cleanupCdp undoes the agent's raw-CDP changes and detaches nothing: the
// claim's release does that.
func cleanupCdp(ctx context.Context, state *CdpState, tab *cdp.Tab) {
	if tab == nil || tab.Closed() {
		return
	}
	if state.Paused || state.Enabled["Debugger"] {
		_, _ = tab.Send(ctx, "Debugger.setBreakpointsActive", map[string]any{"active": false})
		_, _ = tab.Send(ctx, "Debugger.resume", nil)
	}
	for domain := range state.Enabled {
		_, _ = tab.Send(ctx, domain+".disable", nil)
	}
}

// --- Files ---

func runPDF(ctx context.Context, c *stepContext) error {
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	data, err := printToPDF(ctx, state.Tab, c.args.Switches["landscape"])
	if err != nil {
		return err
	}
	path, ok := c.args.Value("path")
	if !ok {
		if positional, ok := c.args.Arg(0); ok {
			path = positional
		}
	}
	if path == "" {
		title := "page"
		var pageTitle string
		if err := state.Tab.Eval(ctx, "document.title", &pageTitle); err == nil && pageTitle != "" {
			title = pageTitle
		}
		path = title + ".pdf"
	}
	exclusive := false
	if _, explicit := c.args.Value("path"); !explicit {
		exclusive = !c.args.Switches["landscape"] && len(c.args.Positional) == 0
	}
	if err := saveFileUnique(ctx, c.session.IO, path, data, exclusive && len(c.args.Positional) == 0); err != nil {
		return err
	}
	c.Note("saved " + path)
	return nil
}

func runDownload(ctx context.Context, c *stepContext) error {
	raw, err := positional(c.args, 0, "<url|target>")
	if err != nil {
		return err
	}
	state, err := c.tabState(ctx)
	if err != nil {
		return err
	}
	url := raw
	name := ""
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "?") {
		target, err := ParseTarget(raw)
		if err != nil {
			return err
		}
		res := Resolver{Tab: state.Tab, Refs: state.BackendNodeID}
		objectID, err := res.One(ctx, target, FindTimeout)
		if err != nil {
			return err
		}
		var link struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if err := evalOnObject(ctx, state.Tab, objectID, `function(){ return { url: this.href || this.src || '', name: this.getAttribute('download') || '' } }`, &link); err != nil {
			return err
		}
		if link.URL == "" {
			return stepErrorf("%s has no href or src to download", raw)
		}
		url = link.URL
		name = link.Name
	}
	file, err := fetchFile(ctx, state.Tab, url)
	if err != nil {
		return err
	}
	if name != "" {
		file.Name = name
	}
	data, err := decodeBase64(file.Base64)
	if err != nil {
		return err
	}
	if len(data) > DownloadMaxBytes {
		return stepErrorf("The file is over %d MB", DownloadMaxBytes/1024/1024)
	}
	path, ok := c.args.Value("path")
	if !ok {
		path = file.Name
		if path == "" {
			path = "download"
		}
	}
	if err := saveFileUnique(ctx, c.session.IO, path, data, false); err != nil {
		return err
	}
	c.Note(fmt.Sprintf("saved %s (%d KB)", path, len(data)/1024))
	return nil
}

// --- Help ---

func runHelp(ctx context.Context, c *stepContext) error {
	if len(c.args.Positional) == 0 {
		var lines []string
		for _, def := range commands {
			if def.Capability != "" && !contains(c.session.Capabilities, def.Capability) {
				continue
			}
			if def.Usage == "" {
				continue
			}
			lines = append(lines, def.Usage)
		}
		lines = append(lines, "", TARGETS_HELP)
		c.Print(strings.Join(lines, "\n"))
		return nil
	}
	name := c.args.Positional[0].Text
	def := commandsByName[name]
	if def == nil {
		return stepErrorf("Unknown command %s. %s", name, unknownCommandHint(name))
	}
	c.Print(def.Usage + "\n\n" + def.Help + "\n\n" + TARGETS_HELP)
	return nil
}

// --- Helpers ---

const TARGETS_HELP = "Targets: a ref from snapshot (e12), a locator (getByRole('button', { name: 'Submit' }), getByLabel, getByText, getByPlaceholder, getByTestId, .first(), .nth(i), .filter({ hasText }), locator('css')), or a CSS selector (quote it if it has spaces)."

const instructions = brand.Name + " controls the user's Chrome through the `browser` tool:\n" +
	"1. Act in batches: put every step you can predict in one call (targets like getByLabel('Email') work without a snapshot).\n" +
	"2. Look with `snapshot -i` or `find \"<text>\"`; read a whole page with `get text --max all`.\n" +
	"3. Re-snapshot after navigating: refs from an older page fail.\n" +
	"4. Wait for what the page gives you (`wait --url`, a role target, `wait --timeout <ms>`), not a guessed string; if the page asks the user to sign in, stop and ask them.\n" +
	"5. A call stops at its first error. Close the tab you opened, even after a failed step, with `tab close <id>` in a call of its own; it only closes tabs you opened."

func browserTool(capabilities []string) Tool {
	everyday := []string{
		"goto <url> | back | forward | reload",
		"click <target>",
		`fill <target> "<text>" [--submit]`,
		`type "<text>"   # key by key, into the focused field`,
		"press <key>",
		`select <target> "<value>"`,
		"check <target> | uncheck <target>",
		`wait <ms> | wait --text "<text>" | wait <target>   # --timeout <ms>, --url <glob>, --gone`,
		"tab list | tab new [<url>] | tab select <id> | tab close",
		"snapshot [<target>] [-i] [--depth N]",
		`find "<text>"`,
		"get text|value|count|url|title [<target>]",
		"screenshot [<target>]",
		`eval "<js function>"   # in the page`,
	}
	more := []string{"scroll", "hover", "drag", "upload", "download", "pdf", "dialog", "console", "requests", "request"}
	for _, capability := range capabilities {
		if capability == "cdp" {
			more = append(more, "cdp")
		}
	}
	description := strings.Join(append(append([]string{
		"Controls the user's browser: a signed-in site, a page that needs its JavaScript, or a UI to click or type in; for a static page, the app's web fetch is cheaper. One command per line, stopping at the first error. Actions print one ✓ line, reads their output. Do many steps per call.",
	}, everyday...), "More (help <command>): "+strings.Join(more, ", "),
		"Targets: e12, getByRole('button', { name: 'Go' }), getByLabel/getByText/getByPlaceholder('…'), or CSS.",
		`Quote text with spaces.`,
	), "\n")
	return Tool{
		Name:        "browser",
		Description: description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"commands": map[string]any{"type": "string", "description": "Command lines, one per line"},
			},
			"required": []string{"commands"},
		},
		Annotations: map[string]any{
			"title": "Browser", "readOnlyHint": false, "destructiveHint": true, "openWorldHint": true,
		},
	}
}

func readCapArg(args ParsedArgs) (int, error) {
	value, ok := args.Value("max")
	if !ok || value == "" {
		return ReadCap, nil
	}
	return ReadCapFor(value, true)
}

func flagValue(args ParsedArgs, name string) string {
	value, _ := args.Value(name)
	return value
}

func positionalTexts(tokens []Token) []string {
	texts := make([]string, len(tokens))
	for i, token := range tokens {
		texts[i] = token.Text
	}
	return texts
}

func activeElement(ctx context.Context, tab *cdp.Tab) (string, error) {
	result, err := tab.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "document.activeElement && document.activeElement !== document.body ? document.activeElement : null",
		"returnByValue": false,
	})
	if err != nil {
		return "", err
	}
	return objectIDFromResult(result)
}

func evalOnObject(ctx context.Context, tab *cdp.Tab, objectID, declaration string, out any) error {
	result, err := callFunction(ctx, tab, objectID, declaration, true)
	if err != nil {
		return err
	}
	var reply struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return err
	}
	return decodeJSONValue(reply.Result.Value, out)
}

func valueFromCall(result json.RawMessage) (any, error) {
	var reply struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return nil, err
	}
	return reply.Result.Value, nil
}

// settle runs an action, then waits for a navigation it started within 200 ms
// to load, so the next step sees the new page.
func settle(ctx context.Context, tab *cdp.Tab, action func() error) (bool, error) {
	navigated := make(chan struct{}, 1)
	unsubscribe := tab.On("Page.frameNavigated", func(_ string, params json.RawMessage) {
		var event struct {
			Frame struct {
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &event) == nil && event.Frame.ParentID == "" {
			select {
			case navigated <- struct{}{}:
			default:
			}
		}
	})
	defer unsubscribe()
	if err := action(); err != nil {
		return false, err
	}
	select {
	case <-navigated:
	case <-time.After(NavigationStart):
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return !waitForLoad(ctx, tab, NavigationLoad), nil
}

func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	return filepath.Abs(path)
}

func saveFile(ctx context.Context, io SessionIO, path string, data []byte, exclusive bool) error {
	_, err := io.WriteFile(ctx, path, data, exclusive)
	return err
}

// saveFileUnique avoids overwriting an existing file for paths the agent did
// not name explicitly.
func saveFileUnique(ctx context.Context, io SessionIO, path string, data []byte, exclusive bool) error {
	if !exclusive {
		return saveFile(ctx, io, path, data, false)
	}
	for attempt := 0; attempt < 100; attempt++ {
		candidate := path
		if attempt > 0 {
			extension := filepath.Ext(path)
			stem := strings.TrimSuffix(path, extension)
			candidate = fmt.Sprintf("%s (%d)%s", stem, attempt, extension)
		}
		if _, err := io.WriteFile(ctx, candidate, data, true); err == nil {
			return nil
		} else if !strings.Contains(strings.ToLower(err.Error()), "exist") {
			return err
		}
	}
	return stepErrorf("Could not find a free file name for %s", path)
}

func decodeBase64(text string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(text)
}

func headerLines(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, name+": "+headers[name])
	}
	return lines
}

func roundSeconds(d time.Duration) string {
	seconds := d.Seconds()
	if seconds >= 1 {
		return fmt.Sprintf("%.0f s", seconds)
	}
	return fmt.Sprintf("%.0f ms", float64(d.Milliseconds()))
}

func firstLine(text string) string { return firstLineOf(text) }

var _ = extproto.MethodCDP
