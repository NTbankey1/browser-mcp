// Package engine is the native command engine behind the `browser` tool: one
// session per AI app's thin process, which tab each uses, and the tool's
// output policy (one ✓ line per action, read output inline, ✗ step N on the
// first error, a Page: trailer). Every line is parsed before the first runs,
// so a call with a bad line runs none of its steps.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/browser"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
)

// Messages the model sees.
const (
	PausedMessage = browser.PausedMessage
	NoTabMessage  = "There's no tab yet: use `goto <url>`, or `tab list` and `tab select <id>` to use one of the user's tabs"
)

// Defaults.
const (
	DefaultIdleRelease = 10 * time.Minute
	StepDefault        = 30 * time.Second
	StepHeadroom       = 5 * time.Second
	OpenedTabWait      = 5 * time.Second
)

// Content is one part of a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallToolResult is the MCP tool result.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Tool is an MCP tool definition.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// SessionIO touches files where the AI app runs, so relative paths resolve
// against its MCP roots.
type SessionIO interface {
	ReadFile(ctx context.Context, path string) (name, mimeType string, data []byte, err error)
	WriteFile(ctx context.Context, path string, data []byte, exclusive bool) (string, error)
}

// Session is one thin process.
type Session struct {
	ID           string
	App          extproto.AppInfo
	Capabilities []string
	IO           SessionIO

	current       int
	pending       int
	hasPending    bool
	tabs          map[int]bool
	opened        map[int]bool
	lastCall      time.Time
	openedTabs    []openedTab
	browser       string
	browserChosen bool
}

type openedTab struct {
	tabID int
	url   string
}

// Options configure an engine.
type Options struct {
	Browsers    *browser.Registry
	Log         *slog.Logger
	IdleRelease time.Duration
	StepLimit   time.Duration
}

// Engine runs sessions and commands.
type Engine struct {
	browsers    *browser.Registry
	log         *slog.Logger
	idleRelease time.Duration
	stepLimit   time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	states   map[int]*TabState
	locks    *keyedMutex
	inFlight atomic.Int64
	stopped  chan struct{}
}

// New creates an engine and registers it as the browser registry's sink.
func New(options Options) *Engine {
	if options.IdleRelease <= 0 {
		options.IdleRelease = DefaultIdleRelease
	}
	if options.StepLimit <= 0 {
		options.StepLimit = StepDefault
	}
	engine := &Engine{
		browsers:    options.Browsers,
		log:         options.Log,
		idleRelease: options.IdleRelease,
		stepLimit:   options.StepLimit,
		sessions:    map[string]*Session{},
		states:      map[int]*TabState{},
		locks:       &keyedMutex{locks: map[string]*keyedMutexEntry{}},
		stopped:     make(chan struct{}),
	}
	if options.Browsers != nil {
		options.Browsers.SetSink(engine)
	}
	go engine.idleLoop()
	return engine
}

// --- browser.Sink ---

// State is called when the tab list or paused changed.
func (e *Engine) State() { e.pruneClosed() }

// Released is called when a claimed tab went away.
func (e *Engine) Released(tabID int, reason string) {
	e.mu.Lock()
	state := e.states[tabID]
	delete(e.states, tabID)
	e.mu.Unlock()
	if state != nil {
		state.dispose()
	}
}

// TabAssigned is called when the user drags a tab into an app's group.
func (e *Engine) TabAssigned(tabID int, appID *string) {
	if appID == nil {
		return
	}
	e.mu.Lock()
	var owner *Session
	for _, session := range e.sessions {
		if session.App.ID == *appID && (owner == nil || session.lastCall.After(owner.lastCall)) {
			owner = session
		}
	}
	if owner != nil {
		owner.pending = tabID
		owner.hasPending = true
	}
	e.mu.Unlock()
}

// TabOpened is called when a page opens a tab.
func (e *Engine) TabOpened(tabID, openerTabID int, url string) {
	e.mu.Lock()
	var owner *Session
	for _, session := range e.sessions {
		if session.current == openerTabID || session.tabs[openerTabID] {
			owner = session
			break
		}
	}
	if owner != nil {
		owner.tabs[tabID] = true
		owner.opened[tabID] = true
		owner.openedTabs = append(owner.openedTabs, openedTab{tabID: tabID, url: url})
	}
	e.mu.Unlock()
	if owner != nil {
		_ = e.browsers.GroupTab(context.Background(), tabID, &owner.App)
	}
}

// Connection is called when a browser connects or disconnects.
func (e *Engine) Connection(connected bool) {}

// --- Sessions ---

// OpenSession starts a session.
func (e *Engine) OpenSession(id string, app extproto.AppInfo, capabilities []string, io SessionIO) {
	e.mu.Lock()
	e.sessions[id] = &Session{
		ID:           id,
		App:          app,
		Capabilities: capabilities,
		IO:           io,
		current:      0,
		hasPending:   false,
		tabs:         map[int]bool{},
		opened:       map[int]bool{},
		lastCall:     time.Now(),
	}
	e.mu.Unlock()
}

// CloseSession ends a session and releases its tabs.
func (e *Engine) CloseSession(ctx context.Context, id string) {
	e.mu.Lock()
	session := e.sessions[id]
	delete(e.sessions, id)
	e.mu.Unlock()
	if session == nil || e.browsers == nil {
		return
	}
	for tabID := range session.tabs {
		if !e.inUse(tabID) {
			e.releaseTab(ctx, tabID)
		}
	}
}

// Busy reports whether any call is in flight.
func (e *Engine) Busy() bool { return e.inFlight.Load() > 0 }

// Close stops the idle loop.
func (e *Engine) Close() {
	select {
	case <-e.stopped:
	default:
		close(e.stopped)
	}
}

// Tools returns the tool definitions for a session.
func (e *Engine) Tools(sessionID string) []Tool {
	e.mu.Lock()
	capabilities := []string{}
	if session := e.sessions[sessionID]; session != nil {
		capabilities = session.Capabilities
	}
	e.mu.Unlock()
	return []Tool{browserTool(capabilities)}
}

// Instructions is the MCP server's `instructions`.
func (e *Engine) Instructions() string { return instructions }

// --- Calls ---

// CallTool runs a `tools/call`.
func (e *Engine) CallTool(ctx context.Context, sessionID, name string, args map[string]any) (CallToolResult, error) {
	e.mu.Lock()
	session := e.sessions[sessionID]
	e.mu.Unlock()
	if session == nil {
		return errorResult("Unknown session " + sessionID), nil
	}
	if name != "browser" {
		return errorResult("Unknown tool " + name), nil
	}
	input, failure := commandsArgument(args)
	if failure != "" {
		return errorResult(failure), nil
	}
	parsed := ParseCommands(input)
	if len(parsed) == 0 {
		return errorResult("No commands. Run `help`"), nil
	}
	checked := make([]checkedLine, 0, len(parsed))
	for _, line := range parsed {
		result, err := checkLine(line, session.Capabilities)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		checked = append(checked, result)
	}
	e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	e.mu.Lock()
	session.lastCall = time.Now()
	e.mu.Unlock()

	releaseSession := e.locks.lock("session:" + session.ID)
	defer releaseSession()
	tabLock := &tabLock{}
	defer tabLock.releaseLock()
	return e.run(ctx, session, checked, tabLock), nil
}

func (e *Engine) run(ctx context.Context, session *Session, lines []checkedLine, tabLock *tabLock) CallToolResult {
	e.takePending(session)
	onlyHelp := true
	for _, line := range lines {
		if line.def.Name != "help" {
			onlyHelp = false
		}
	}
	if !onlyHelp {
		if e.browsers != nil {
			e.browsers.WhenReady(ctx, 10*time.Second)
			if reason := e.browsers.UnavailableReason(); reason != "" {
				return errorResult(reason)
			}
			if e.browsers.Paused() {
				return errorResult(PausedMessage)
			}
			e.browsers.NoteActivity(session.App.ID, session.browser)
		}
	}
	e.mu.Lock()
	session.openedTabs = nil
	e.mu.Unlock()
	out := []string{}
	var images []Content
	e.lockTab(ctx, session, tabLock)
	acted := false
	for _, line := range lines {
		if !line.def.Reads || line.def.Name == "eval" {
			acted = true
		}
	}
	startURL, startTitle := e.pageInfo(ctx, session, acted)
	navigated := false
	failed := false
	stoppedBy := ""
	budget := ResponseCap - StatusReserve
	pushPrinted := func(printed []string) {
		if len(printed) == 0 {
			return
		}
		text := strings.Join(printed, "\n")
		if len(text) <= budget {
			out = append(out, text)
			budget -= len(text) + 1
			return
		}
		hint := fmt.Sprintf("the response is capped at %d characters; read less per call", ResponseCap)
		room := budget - len(hint) - 40
		if room > 200 {
			out = append(out, fmt.Sprintf("%s\n… cut at %d of %d characters: %s", text[:room], room, len(text), hint))
		} else {
			out = append(out, fmt.Sprintf("… %d characters left out: %s", len(text), hint))
		}
		budget = 0
	}

	for _, line := range lines {
		var printed, notes []string
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout(line, e.stepLimit))
		execute := func() error {
			e.lockTab(stepCtx, session, tabLock)
			tabID := e.currentTab(session)
			state := e.stateForID(tabID)
			if state != nil && state.Dialog() != nil && !line.def.WhileDialog {
				return stepErrorf("%s is open. Answer it with `dialog accept` or `dialog dismiss` first", state.Dialog().Label())
			}
			tracksURL := !line.def.Reads || line.def.Name == "eval"
			before := ""
			if tracksURL && tabID != 0 {
				before, _ = e.quickURL(stepCtx, tabID)
			}
			step := &stepContext{engine: e, session: session, args: line.args, line: line,
				printed: &printed, notes: &notes, images: &images}
			if err := line.def.Run(stepCtx, step); err != nil {
				// A native dialog blocks the tab's CDP session until it is
				// answered, which surfaces as a timeout: name the real cause.
				if current := e.stateForID(e.currentTab(session)); current != nil && current.Dialog() != nil {
					return stepErrorf("%s opened. Answer it with `dialog accept` or `dialog dismiss`", current.Dialog().Label())
				}
				return err
			}
			if line.def.Navigates {
				navigated = true
			}
			tabID = e.currentTab(session)
			state = e.stateForID(tabID)
			dialogOpen := state != nil && state.Dialog() != nil
			if dialogOpen {
				// A native dialog blocks this tab's CDP session until it is
				// answered, so stop here: any read, even the URL, would wait.
				notes = append(notes, fmt.Sprintf("%s opened. Answer it with `dialog accept` or `dialog dismiss`", state.Dialog().Label()))
				stoppedBy = "a dialog is open"
				return nil
			}
			if !line.def.Navigates && tracksURL && tabID != 0 && before != "" {
				// Its own short deadline: if the step opened a dialog that the
				// engine's event hasn't reported yet, this probe waits on a
				// blocked session otherwise.
				probeCtx, cancelProbe := context.WithTimeout(stepCtx, 2*time.Second)
				after, probeErr := e.quickURL(probeCtx, tabID)
				cancelProbe()
				if probeErr != nil {
					// Give the dialog's event a moment to arrive, then say so.
					for deadline := time.Now().Add(700 * time.Millisecond); time.Now().Before(deadline); {
						if state != nil && state.Dialog() != nil {
							dialogOpen = true
							break
						}
						time.Sleep(25 * time.Millisecond)
					}
				} else if after != "" && after != before {
					navigated = true
					notes = append(notes, "→ "+shortURL(after, before))
				}
			}
			for _, opened := range e.takeOpenedTabs(session) {
				notes = append(notes, e.openedNote(opened))
			}
			if dialogOpen || (state != nil && state.Dialog() != nil) {
				state = e.stateForID(tabID)
				notes = append(notes, fmt.Sprintf("%s opened. Answer it with `dialog accept` or `dialog dismiss`", state.Dialog().Label()))
				stoppedBy = "a dialog is open"
			}
			return nil
		}
		err := execute()
		// A read can be re-run safely when the debugger re-attached (a new
		// Page, or the browser came back): the tab is fine. A step that acts
		// isn't repeated, since it may have acted already.
		for attempt := 0; err != nil && line.def.Reads && attempt < 3 && isReattachError(err); attempt++ {
			tabID := e.currentTab(session)
			if tabID == 0 || e.browsers.TabClosed(tabID) {
				break
			}
			e.Released(tabID, "re-attached")
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
			}
			printed, notes = nil, nil
			err = execute()
		}
		cancel()
		if err != nil {
			failed = true
			pushPrinted(printed)
			out = append(out, fmt.Sprintf("✗ step %d: %s — %s", line.line.Step, line.echo, ErrorMessage(err)))
			out = append(out, notRun(line.line.Step, len(lines))...)
			break
		}
		lineText := "✓ " + line.echo
		if len(notes) > 0 {
			lineText += " — " + strings.Join(notes, "; ")
		}
		out = append(out, lineText)
		pushPrinted(printed)
		if stoppedBy != "" {
			out = append(out, notRun(line.line.Step, len(lines), stoppedBy)...)
			break
		}
	}

	endURL, endTitle := e.pageInfo(ctx, session, acted || navigated || failed)
	if endURL != "" && (failed || startURL != endURL || startTitle != endTitle) {
		out = append(out, fmt.Sprintf("Page: %s — %s", endURL, quote(Truncate(endTitle, 200))))
	}
	if paused := e.pausedDebuggers(session); paused != "" {
		out = append(out, paused)
	}
	content := []Content{{Type: "text", Text: strings.Join(out, "\n")}}
	content = append(content, images...)
	return CallToolResult{Content: content, IsError: failed}
}

// --- Tab state and locks ---

func (e *Engine) stateForID(tabID int) *TabState {
	if tabID == 0 {
		return nil
	}
	e.mu.Lock()
	state := e.states[tabID]
	e.mu.Unlock()
	return state
}

// tabState claims the session's current tab and returns its state, set up on
// first use after each claim.
func (e *Engine) tabState(ctx context.Context, session *Session) (*TabState, error) {
	tabID := e.currentTab(session)
	if tabID == 0 {
		return nil, stepErrorf(NoTabMessage)
	}
	tab, err := e.browsers.Page(ctx, tabID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	state := e.states[tabID]
	e.mu.Unlock()
	if state == nil || state.Tab != tab {
		if state != nil {
			state.dispose()
		}
		state = newTabState(tabID, tab)
		state.watch()
		e.mu.Lock()
		e.states[tabID] = state
		e.mu.Unlock()
		emulateFocus(ctx, tab)
		_, _ = tab.Send(ctx, "Page.enable", nil)
		_, _ = tab.Send(ctx, "Runtime.enable", nil)
		_, _ = tab.Send(ctx, "Network.enable", nil)
	}
	state.mu.Lock()
	state.lastUsed = time.Now()
	state.mu.Unlock()
	return state, nil
}

func (e *Engine) currentTab(session *Session) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if session.current != 0 && e.browsers.TabClosed(session.current) {
		e.forgetLocked(session, session.current)
	}
	return session.current
}

func (e *Engine) takePending(session *Session) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !session.hasPending {
		return
	}
	tabID := session.pending
	session.hasPending = false
	if tabID == 0 || e.browsers.TabClosed(tabID) {
		return
	}
	session.tabs[tabID] = true
	session.current = tabID
}

func (e *Engine) adopt(session *Session, tabID int, opened bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	session.tabs[tabID] = true
	if opened {
		session.opened[tabID] = true
	}
	session.current = tabID
	for _, tab := range e.browsers.Tabs() {
		if tab.TabID == tabID {
			session.browser = tab.Browser
			session.browserChosen = false
		}
	}
}

func (e *Engine) forgetLocked(session *Session, tabID int) {
	delete(session.tabs, tabID)
	delete(session.opened, tabID)
	if session.current == tabID {
		session.current = 0
	}
}

func (e *Engine) inUse(tabID int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, session := range e.sessions {
		if session.tabs[tabID] {
			return true
		}
	}
	return false
}

func (e *Engine) pruneClosed() {
	if e.browsers == nil {
		return
	}
	e.mu.Lock()
	sessions := make([]*Session, 0, len(e.sessions))
	for _, session := range e.sessions {
		sessions = append(sessions, session)
	}
	states := make(map[int]*TabState, len(e.states))
	for id, state := range e.states {
		states[id] = state
	}
	e.mu.Unlock()
	for _, session := range sessions {
		e.mu.Lock()
		for tabID := range session.tabs {
			if e.browsers.TabClosed(tabID) {
				e.forgetLocked(session, tabID)
			}
		}
		e.mu.Unlock()
	}
	for tabID, state := range states {
		if e.browsers.TabClosed(tabID) {
			e.Released(tabID, "the tab closed")
			state.dispose()
		}
	}
}

func (e *Engine) releaseTab(ctx context.Context, tabID int) {
	e.mu.Lock()
	state := e.states[tabID]
	delete(e.states, tabID)
	e.mu.Unlock()
	if state != nil {
		state.dispose()
		if state.cdp != nil {
			cleanupCdp(ctx, state.cdp, state.Tab)
		}
	}
	if e.browsers.IsClaimed(tabID) {
		_ = e.browsers.Release(ctx, tabID)
	}
}

func (e *Engine) idleLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopped:
			return
		case now := <-ticker.C:
			_ = now
			threshold := time.Now().Add(-e.idleRelease)
			e.mu.Lock()
			var idle []int
			for tabID, state := range e.states {
				state.mu.Lock()
				lastUsed := state.lastUsed
				state.mu.Unlock()
				if lastUsed.Before(threshold) && !e.locks.held(fmt.Sprintf("tab:%d", tabID)) {
					idle = append(idle, tabID)
				}
			}
			e.mu.Unlock()
			for _, tabID := range idle {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				e.releaseTab(ctx, tabID)
				cancel()
			}
		}
	}
}

// --- Tab locking ---

type tabLock struct {
	tabID   int
	release func()
}

func (l *tabLock) releaseLock() {
	if l.release != nil {
		l.release()
		l.release = nil
	}
	l.tabID = 0
}

func (e *Engine) lockTab(ctx context.Context, session *Session, lock *tabLock) {
	for {
		tabID := e.currentTab(session)
		if tabID == lock.tabID {
			return
		}
		lock.releaseLock()
		lock.tabID = tabID
		if tabID == 0 {
			return
		}
		lock.release = e.locks.lock(fmt.Sprintf("tab:%d", tabID))
		if e.currentTab(session) == tabID {
			return
		}
	}
}

type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	entry := k.locks[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

func (k *keyedMutex) held(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	entry := k.locks[key]
	if entry == nil {
		return false
	}
	if entry.mu.TryLock() {
		entry.mu.Unlock()
		return false
	}
	return true
}

// --- Output helpers ---

func (e *Engine) pageInfo(ctx context.Context, session *Session, withTitle bool) (string, string) {
	tabID := e.currentTab(session)
	if tabID == 0 {
		return "", ""
	}
	url, _ := e.quickURL(ctx, tabID)
	title := ""
	if withTitle {
		title, _ = e.quickTitle(ctx, tabID)
	}
	return url, title
}

func (e *Engine) quickURL(ctx context.Context, tabID int) (string, error) {
	if state := e.stateForID(tabID); state != nil && !state.Tab.Closed() {
		var href string
		err := state.Tab.Eval(ctx, "location.href", &href)
		return href, err
	}
	for _, tab := range e.browsers.Tabs() {
		if tab.TabID == tabID {
			return tab.URL, nil
		}
	}
	return "", nil
}

func (e *Engine) quickTitle(ctx context.Context, tabID int) (string, error) {
	if state := e.stateForID(tabID); state != nil && !state.Tab.Closed() {
		titleCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var title string
		err := state.Tab.Eval(titleCtx, "document.title", &title)
		return title, err
	}
	for _, tab := range e.browsers.Tabs() {
		if tab.TabID == tabID {
			return tab.Title, nil
		}
	}
	return "", nil
}

func (e *Engine) takeOpenedTabs(session *Session) []openedTab {
	e.mu.Lock()
	opened := session.openedTabs
	session.openedTabs = nil
	e.mu.Unlock()
	return opened
}

func (e *Engine) openedNote(opened openedTab) string {
	for i := 0; i < 10; i++ {
		if tab, ok := e.browsers.FindTab(opened.tabID); ok && tab.Title != "" {
			return fmt.Sprintf("opened tab %d %s", opened.tabID, quote(Truncate(tab.Title, 80)))
		}
		time.Sleep(100 * time.Millisecond)
	}
	url := opened.url
	if url == "" {
		url = "about:blank"
	}
	return fmt.Sprintf("opened tab %d (%s)", opened.tabID, url)
}

func (e *Engine) pausedDebuggers(session *Session) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var paused []string
	for tabID := range session.tabs {
		state := e.states[tabID]
		if state == nil {
			continue
		}
		state.mu.Lock()
		isPaused := state.cdp != nil && state.cdp.Paused
		state.mu.Unlock()
		if isPaused {
			paused = append(paused, fmt.Sprintf("%d", tabID))
		}
	}
	if len(paused) == 0 {
		return ""
	}
	return fmt.Sprintf("⏸ The debugger is paused in tab %s; the page is frozen until `cdp Debugger.resume`", strings.Join(paused, ", "))
}

// --- Helpers ---

func errorResult(text string) CallToolResult {
	return CallToolResult{Content: []Content{{Type: "text", Text: text}}, IsError: true}
}

func shortURL(next, previous string) string {
	nextURL, err := url.Parse(next)
	if err != nil {
		return next
	}
	previousURL, err := url.Parse(previous)
	if err != nil || nextURL.Scheme != previousURL.Scheme || nextURL.Host != previousURL.Host || nextURL.Host == "" {
		return next
	}
	return nextURL.Path + func() string {
		if nextURL.RawQuery != "" {
			return "?" + nextURL.RawQuery
		}
		return ""
	}() + func() string {
		if nextURL.Fragment != "" {
			return "#" + nextURL.Fragment
		}
		return ""
	}()
}

func notRun(step, total int, reason ...string) []string {
	if step >= total {
		return nil
	}
	rangeText := fmt.Sprintf("Steps %d–%d", step+1, total)
	if step+1 == total {
		rangeText = fmt.Sprintf("Step %d", total)
	}
	suffix := ""
	if len(reason) > 0 && reason[0] != "" {
		suffix = " (" + reason[0] + ")"
	}
	return []string{rangeText + " not run" + suffix}
}

// isReattachError reports an error that means the debugger let go of the tab
// and the engine can claim it again.
func isReattachError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"isn't attached", "not attached", "detached while handling",
		"browser disconnected from browser mcp", "target closed",
		"the tab detached", "detached: detached",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

// normalizeURL adds a scheme when the agent leaves it off.
func normalizeURL(value string) string {
	lower := strings.ToLower(value)
	for _, scheme := range []string{"http://", "https://", "file://", "data:", "about:", "chrome-extension://", "view-source:"} {
		if strings.HasPrefix(lower, scheme) {
			return value
		}
	}
	if strings.HasPrefix(lower, "localhost") || strings.HasPrefix(lower, "127.") || strings.HasPrefix(lower, "[::1]") {
		return "http://" + value
	}
	return "https://" + value
}

func urlMatcher(pattern string) func(string) bool {
	if !strings.Contains(pattern, "*") {
		return func(value string) bool { return strings.Contains(value, pattern) }
	}
	var builder strings.Builder
	builder.WriteString("^")
	parts := strings.Split(pattern, "**")
	for i, part := range parts {
		if i > 0 {
			builder.WriteString(".*")
		}
		pieces := strings.Split(part, "*")
		for j, piece := range pieces {
			if j > 0 {
				builder.WriteString("[^/]*")
			}
			builder.WriteString(regexp.QuoteMeta(piece))
		}
	}
	builder.WriteString("$")
	compiled, err := regexp.Compile(builder.String())
	if err != nil {
		return func(value string) bool { return strings.Contains(value, pattern) }
	}
	return compiled.MatchString
}

// DefaultStepTimeout bounds a step that names no timeout of its own, so a CDP
// call that never answers (a native dialog blocks the tab) fails instead of
// hanging the call.
const DefaultStepTimeout = 30 * time.Second

func stepTimeout(line checkedLine, limit time.Duration) time.Duration {
	own := time.Duration(0)
	if line.def.Timeout != nil {
		own = line.def.Timeout(line.args)
	}
	if value, ok := line.args.Value("timeout"); ok {
		var ms int
		if _, err := fmt.Sscanf(value, "%d", &ms); err == nil && ms >= 0 {
			flagTimeout := time.Duration(ms) * time.Millisecond
			if own == 0 || flagTimeout < own {
				own = flagTimeout
			}
		}
	}
	if own == 0 {
		if limit > 0 {
			return limit
		}
		return DefaultStepTimeout
	}
	return own + StepHeadroom
}

// checkedLine is a parsed line that passed the pre-run checks.
type checkedLine struct {
	line CommandLine
	def  *CommandDef
	args ParsedArgs
	echo string
}

// checkLine checks what a line can get wrong without a page.
func checkLine(line CommandLine, capabilities []string) (checkedLine, error) {
	name := CommandName(line.Source)
	def := commandsByName[name]
	// Until a line parses, commands that take typed text echo only their
	// name; every other command echoes its whole line.
	echo := name
	if def == nil || def.Echo == nil {
		echo = Truncate(line.Source, EchoMax)
	}
	if def == nil {
		return checkedLine{}, fmt.Errorf("✗ step %d: %s — Unknown command %s. %s",
			line.Step, echo, name, unknownCommandHint(name))
	}
	if def.Capability != "" && !contains(capabilities, def.Capability) {
		return checkedLine{}, fmt.Errorf("✗ step %d: %s — `%s` is off. Start @browsermcp/mcp with `--caps %s` to turn it on",
			line.Step, echo, def.Name, def.Capability)
	}
	tokens, err := Tokenize(line.Source)
	if err != nil {
		return checkedLine{}, fmt.Errorf("✗ step %d: %s — %s", line.Step, echo, err.Error())
	}
	args, err := ParseArgs(name, tokens[1:], def.Flags)
	if err != nil {
		return checkedLine{}, fmt.Errorf("✗ step %d: %s — %s", line.Step, echo, err.Error())
	}
	if def.Echo != nil {
		echo = Truncate(def.Echo(args), EchoMax)
	} else {
		echo = Truncate(line.Source, EchoMax)
	}
	if def.MaxArgs >= 0 && len(args.Positional) > def.MaxArgs {
		takes := "no arguments"
		if def.MaxArgs > 0 {
			takes = fmt.Sprintf("at most %d argument(s)", def.MaxArgs)
		}
		return checkedLine{}, fmt.Errorf("✗ step %d: %s — %s takes %s, got %d. Quote text with spaces in double quotes; run `help %s`",
			line.Step, echo, def.Name, takes, len(args.Positional), def.Name)
	}
	return checkedLine{line: line, def: def, args: args, echo: Truncate(echo, EchoMax)}, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func availableCommandNames(capabilities []string) []string {
	var names []string
	for _, def := range commands {
		if def.Capability == "" || contains(capabilities, def.Capability) {
			names = append(names, def.Name)
		}
	}
	return names
}

// --- Call arguments and mistyped commands ---

// commandsShape says what the tool's one argument is, for a call that sent
// something else.
const commandsShape = "`commands` is a string of command lines, one per line, e.g. \"goto https://example.com\\nsnapshot -i\""

// commandsArgument reads the tool call's `commands`, naming the argument when
// the call sends it under another key or as another type: a model that sends
// `command` or an array of lines gets told what to send, not what a schema is.
func commandsArgument(args map[string]any) (string, string) {
	if value, present := args["commands"]; present {
		switch typed := value.(type) {
		case string:
			return typed, ""
		case []any:
			lines := make([]string, 0, len(typed))
			for _, entry := range typed {
				line, ok := entry.(string)
				if !ok {
					lines = nil
					break
				}
				lines = append(lines, line)
			}
			if lines != nil {
				return strings.Join(lines, "\n"), ""
			}
			return "", "`commands` is an array with something that isn't a line of text in it; " + commandsShape
		case []string:
			return strings.Join(typed, "\n"), ""
		case nil:
			return "", "`commands` is null; " + commandsShape + ". Run `help` to list the commands"
		default:
			return "", "`commands` is " + jsonTypeName(value) + "; " + commandsShape
		}
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", "No arguments. " + commandsShape + ". Run `help` to list the commands"
	}
	quoted := make([]string, len(keys))
	near := false
	for i, key := range keys {
		quoted[i] = "`" + key + "`"
		switch strings.ToLower(key) {
		case "command", "cmd", "cmds", "commandlines", "lines", "line", "input", "script", "code":
			near = true
		}
	}
	message := "The tool takes one argument, " + strings.Join(quoted, ", ") + "; " + commandsShape
	if near {
		message += ". Did you mean `commands`?"
	} else {
		message += ". Run `help` to list the commands"
	}
	return "", message
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case bool:
		return "a boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return "a number"
	case map[string]any:
		return "an object"
	default:
		return "not a string"
	}
}

// unknownCommandHint names what a mistyped command most likely meant, then
// points at `help` for everything else.
func unknownCommandHint(name string) string {
	suggestions := commandSuggestions(name)
	if len(suggestions) == 0 {
		return "Run `help` to list the commands"
	}
	return "Did you mean " + quotedList(suggestions) + "? Run `help` to list the commands"
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + value + "`"
	}
	switch len(quoted) {
	case 1:
		return quoted[0]
	case 2:
		return quoted[0] + " or " + quoted[1]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	}
}

// commandSuggestions is what a mistyped command name could have meant: a word
// of a multi-word command (`list` is `tab list` and `browser list`), a prefix
// (`snap` is `snapshot`), or a name one or two edits away (`clik` is `click`).
func commandSuggestions(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	var suggestions []string
	seen := map[string]bool{}
	add := func(candidate string) {
		if candidate == "" || candidate == name || seen[candidate] {
			return
		}
		seen[candidate] = true
		suggestions = append(suggestions, candidate)
	}
	for _, def := range commands {
		for _, alternative := range strings.Split(def.Usage, "|") {
			alternative = strings.TrimSpace(strings.SplitN(alternative, "#", 2)[0])
			words := strings.Fields(alternative)
			for i, word := range words {
				if i > 0 && strings.EqualFold(word, name) {
					add(strings.Join(words[:i+1], " "))
					break
				}
			}
		}
	}
	if len(name) >= 3 {
		for _, def := range commands {
			if strings.HasPrefix(def.Name, name) {
				add(def.Name)
			}
		}
	}
	// Names that share the first letter only: `nope` is two edits from `type`
	// but means nothing, and a suggestion that wrong is worse than none.
	best := 3
	var nearest []string
	for _, def := range commands {
		if name[0] != def.Name[0] {
			continue
		}
		switch distance := editDistance(name, def.Name); {
		case distance < best:
			best = distance
			nearest = []string{def.Name}
		case distance == best:
			nearest = append(nearest, def.Name)
		}
	}
	// Two edits are a typo in a long word (`clcik`), not in a short one
	if best == 1 || (best == 2 && len(name) >= 5) {
		for _, candidate := range nearest {
			add(candidate)
		}
	}
	if len(suggestions) > 3 {
		suggestions = suggestions[:3]
	}
	return suggestions
}

// editDistance is the Levenshtein distance between two lowercase words.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

// --- Browser choice ---

func (e *Engine) severalBrowsers() bool {
	return e.browsers != nil && e.browsers.Several()
}

func (e *Engine) tabBrowser(session *Session) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if session.browser == "" {
		return ""
	}
	for _, summary := range e.browsers.Browsers() {
		if summary.ID == session.browser {
			if summary.Usable || session.browserChosen {
				return session.browser
			}
			return ""
		}
	}
	return ""
}

func (e *Engine) findBrowser(name string) (*browser.Browser, error) {
	return e.browsers.BrowserByIDOrLabel(name)
}

// --- Step context ---

type stepContext struct {
	engine  *Engine
	session *Session
	args    ParsedArgs
	line    checkedLine
	printed *[]string
	notes   *[]string
	images  *[]Content
}

func (c *stepContext) Print(text string) { *c.printed = append(*c.printed, text) }
func (c *stepContext) Note(text string)  { *c.notes = append(*c.notes, text) }

func (c *stepContext) Image(data []byte, mimeType string) {
	if len(*c.images) >= MaxImages {
		c.Note(fmt.Sprintf("image left out: at most %d per response, so take more in another call", MaxImages))
		return
	}
	*c.images = append(*c.images, Content{
		Type: "image", Data: base64Encode(data), MimeType: mimeType,
	})
}

// tabState is the current tab's state.
func (c *stepContext) tabState(ctx context.Context) (*TabState, error) {
	return c.engine.tabState(ctx, c.session)
}

// resolver is a target resolver on the current tab.
func (c *stepContext) resolver(ctx context.Context) (Resolver, *TabState, error) {
	state, err := c.tabState(ctx)
	if err != nil {
		return Resolver{}, nil, err
	}
	return Resolver{Tab: state.Tab, Refs: state.BackendNodeID}, state, nil
}

// target parses positional argument index as a target.
func (c *stepContext) target(index int) (Target, error) {
	source, ok := c.args.Arg(index)
	if !ok {
		return Target{}, stepErrorf("Missing <target>")
	}
	return ParseTarget(source)
}

// pageForGoto returns the current tab's session, opening a tab if there is
// none.
func (c *stepContext) pageForGoto(ctx context.Context) (*TabState, error) {
	if c.engine.currentTab(c.session) == 0 {
		tabID, err := c.engine.browsers.CreateTab(ctx, "", c.session.App, c.engine.tabBrowser(c.session))
		if err != nil {
			return nil, err
		}
		c.engine.adopt(c.session, tabID, true)
	}
	return c.tabState(ctx)
}

func base64Encode(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var builder strings.Builder
	builder.Grow((len(data) + 2) / 3 * 4)
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], data[i:])
		builder.WriteByte(alphabet[chunk[0]>>2])
		builder.WriteByte(alphabet[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			builder.WriteByte(alphabet[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			builder.WriteByte('=')
		}
		if n > 2 {
			builder.WriteByte(alphabet[chunk[2]&0x3f])
		} else {
			builder.WriteByte('=')
		}
	}
	return builder.String()
}

var errNoBrowser = errors.New("no browser connected")

// decodeJSONValue decodes a remote value that arrived as JSON.
func decodeJSONValue(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// asInt reads an int argument.
func asInt(text string) (int, bool) {
	var value int
	if _, err := fmt.Sscanf(text, "%d", &value); err != nil {
		return 0, false
	}
	return value, true
}

// tabForID claims a tab by public ID.
func (e *Engine) tabForID(ctx context.Context, tabID int) (*cdp.Tab, error) {
	return e.browsers.Page(ctx, tabID)
}
