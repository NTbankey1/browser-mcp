// Package browser keeps the browsers (Chrome profiles) the daemon knows, the
// tabs they report under public IDs agents can use across browsers, and the
// claimed tabs (attached debugger sessions). It is the native replacement for
// RelayBackend + BrowserSlot, without Playwright or the CDP bridge.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extension"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
)

// Messages the model sees when no browser is usable.
const (
	NoExtensionMessage = "No browser is connected. Install the Browser MCP extension, or update it to 2.0: open chrome://extensions, turn on Developer mode and click Update"
	PausedMessage      = "The user paused Browser MCP. Ask them to click Resume in its popup"
)

// PairingRequiredMessage is what requests answer while the extension waits
// for Allow, optionally naming the code.
func PairingRequiredMessage(code string) string {
	message := "Click Allow in the Browser MCP extension's popup"
	if code != "" {
		return message + ". The code is " + code
	}
	return message
}

// Tab is a tab as agents see it: TabID is public and unique across browsers.
type Tab struct {
	TabID    int     `json:"tabId"`
	Browser  string  `json:"browser"`
	WindowID int     `json:"windowId"`
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Active   bool    `json:"active"`
	Pinned   bool    `json:"pinned"`
	AppID    *string `json:"appId,omitempty"`
	Attached bool    `json:"attached"`

	chromeID int
}

// Browser is one Chrome profile.
type Browser struct {
	ID           string
	Name         string
	UserAgent    string
	DefaultLabel string
	BrowserID    string

	mu           sync.Mutex
	conn         *extension.Client
	windowIDs    map[int]bool
	publicIDs    map[int]int    // Chrome tab ID → public ID
	tabs         map[int]*Tab   // public ID → tab
	urlsWhenLeft map[int]string // Chrome tab ID → URL at disconnect
	label        *string
	lastFocused  *int64
	connectedAt  time.Time
	leftAt       time.Time
	unconfirmed  bool
}

// Label is what agents call the browser: the user's label, else ours.
func (b *Browser) Label() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unconfirmed {
		return b.Name
	}
	if b.label != nil && *b.label != "" {
		return *b.label
	}
	return b.DefaultLabel
}

// Connected reports whether the browser has a live extension connection.
func (b *Browser) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil && b.conn.Open()
}

// Paired reports whether the user clicked Allow.
func (b *Browser) Paired() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil && b.conn.Paired()
}

// Paused reports Stop (or Cancel on the debugging bar).
func (b *Browser) Paused() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil && b.conn.State().Paused
}

// PairingCode is the code its popup shows, while it waits for Allow.
func (b *Browser) PairingCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return ""
	}
	return b.conn.PairingCode()
}

// Usable is connected, paired and not paused.
func (b *Browser) Usable() bool { return b.Connected() && b.Paired() && !b.Paused() }

// Tabs are the tabs the browser reports, under public IDs.
func (b *Browser) Tabs() []Tab {
	b.mu.Lock()
	defer b.mu.Unlock()
	tabs := make([]Tab, 0, len(b.tabs))
	for _, tab := range b.tabs {
		tabs = append(tabs, *tab)
	}
	sort.Slice(tabs, func(i, j int) bool { return tabs[i].TabID < tabs[j].TabID })
	return tabs
}

// Summary is the browser as `browser list` shows it.
type Summary struct {
	ID          string
	Label       string
	Connected   bool
	Pairing     string
	Paused      bool
	Tabs        int
	LastFocused *int64
	ConnectedAt time.Time
	Usable      bool
	Default     bool
}

// Sink receives the browser events the engine reacts to. The registry calls
// it without holding its lock.
type Sink interface {
	State()
	Released(tabID int, reason string)
	TabAssigned(tabID int, appID *string)
	TabOpened(tabID, openerTabID int, url string)
	Connection(connected bool)
}

// Registry is every browser the daemon knows, connected or not.
type Registry struct {
	mu       sync.Mutex
	browsers []*Browser
	byID     map[string]*Browser
	nextID   int
	spareID  int
	sink     Sink
	log      *slog.Logger
	version  string
	waiters  map[chan struct{}]struct{}

	claimsMu sync.Mutex
	claims   map[int]*claim
	tabsMu   sync.Mutex
	// retired public IDs whose Chrome tab is gone, so a closed tab keeps
	// failing as closed instead of naming a new one.
	tombstones map[int]struct{}
}

type claim struct {
	done chan struct{}
	tab  *cdp.Tab
	err  error
}

// Options configure a registry.
type Options struct {
	Version string
	Sink    Sink
	Log     *slog.Logger
}

// New returns a registry.
func New(options Options) *Registry {
	r := &Registry{
		byID:       make(map[string]*Browser),
		nextID:     1,
		spareID:    100_000,
		sink:       options.Sink,
		log:        options.Log,
		tombstones: make(map[int]struct{}),
		claims:     make(map[int]*claim),
		waiters:    make(map[chan struct{}]struct{}),
	}
	return r
}

// SetSink sets the event sink after the engine that receives them exists.
func (r *Registry) SetSink(sink Sink) {
	r.mu.Lock()
	r.sink = sink
	r.mu.Unlock()
}

// AcceptExtension takes over an accepted extension WebSocket: it welcomes it,
// assigns its browser slot and starts routing its events. Port is the port the
// extension dialed (the public one).
func (r *Registry) AcceptExtension(ctx context.Context, ws *websocket.Conn, options extension.Options) (*extension.Client, error) {
	client, err := extension.Accept(ctx, ws, options)
	if err != nil {
		return nil, err
	}
	browser, err := r.assign(client)
	if err != nil {
		client.Close(extproto.CloseAnotherBrowser, tooManyBrowsersReason(extproto.MaxBrowsers))
		return nil, err
	}
	r.wake()
	go r.route(client, browser)
	if r.sink != nil {
		r.sink.Connection(true)
	}
	// Keep the handler (and so the socket) alive for the connection's life
	<-client.Done()
	return client, nil
}

func tooManyBrowsersReason(max int) string {
	return fmt.Sprintf("Browser MCP is connected to %d browsers already", max)
}

func (r *Registry) assign(client *extension.Client) (*Browser, error) {
	hello := client.Hello()
	r.mu.Lock()
	defer r.mu.Unlock()

	// The same profile reconnecting keeps its slot
	if hello.BrowserID != "" {
		for _, browser := range r.browsers {
			if browser.BrowserID == hello.BrowserID {
				return r.take(browser, client, hello), nil
			}
		}
	}
	// Older extensions have no browserId: match the last browser with the
	// same name, user agent and a window in common
	var candidate *Browser
	for _, browser := range r.browsers {
		if browser.matches(hello) {
			if candidate == nil || browser.leftAt.After(candidate.leftAt) {
				candidate = browser
			}
		}
	}
	if candidate != nil {
		return r.take(candidate, client, hello), nil
	}
	if len(r.browsers) >= extproto.MaxBrowsers {
		return nil, errors.New(tooManyBrowsersReason(extproto.MaxBrowsers))
	}
	browser := &Browser{
		ID:           fmt.Sprintf("b%d", r.nextID),
		Name:         hello.Browser.Name,
		UserAgent:    hello.Browser.UserAgent,
		DefaultLabel: hello.Browser.Name,
		BrowserID:    hello.BrowserID,
		windowIDs:    make(map[int]bool),
		publicIDs:    make(map[int]int),
		tabs:         make(map[int]*Tab),
		urlsWhenLeft: make(map[int]string),
		unconfirmed:  hello.BrowserID == "",
	}
	if browser.DefaultLabel == "" {
		browser.DefaultLabel = "Chrome"
	}
	r.nextID++
	// `Chrome`, `Chrome 2`, ... by how many share the name
	count := 0
	for _, other := range r.browsers {
		if other.Name == browser.Name {
			count++
		}
	}
	if count > 0 {
		browser.DefaultLabel = fmt.Sprintf("%s %d", browser.Name, count+1)
	}
	r.browsers = append(r.browsers, browser)
	r.byID[browser.ID] = browser
	return r.take(browser, client, hello), nil
}

func (r *Registry) take(browser *Browser, client *extension.Client, hello extproto.Hello) *Browser {
	browser.mu.Lock()
	previous := browser.conn
	browser.conn = client
	browser.connectedAt = time.Now()
	browser.leftAt = time.Time{}
	browser.windowIDs = make(map[int]bool)
	for _, tab := range hello.State.Tabs {
		browser.windowIDs[tab.WindowID] = true
	}
	if hello.BrowserID != "" {
		browser.BrowserID = hello.BrowserID
	}
	browser.mu.Unlock()
	if previous != nil && previous != client {
		previous.Close(extproto.CloseShuttingDown, "replaced by a newer connection")
	}
	return browser
}

func (b *Browser) matches(hello extproto.Hello) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil && b.conn.Open() {
		return false
	}
	if b.Name != hello.Browser.Name || b.UserAgent != hello.Browser.UserAgent {
		return false
	}
	if len(b.windowIDs) == 0 {
		return true
	}
	for _, tab := range hello.State.Tabs {
		if b.windowIDs[tab.WindowID] {
			return true
		}
	}
	return false
}

// route consumes one connection's events until it closes.
func (r *Registry) route(client *extension.Client, browser *Browser) {
	for {
		var event extproto.Event
		select {
		case event = <-client.Events():
		case <-client.Done():
			r.disconnect(browser, client)
			return
		}
		switch event.Type {
		case extproto.EventState:
			var state extproto.State
			if event.DecodeData(&state) != nil {
				continue
			}
			r.applyState(browser, client, state)
			r.notifyState()
		case extproto.EventCDP:
			var cdpEvent extproto.CDPEvent
			if event.DecodeData(&cdpEvent) != nil {
				continue
			}
			if tab := r.claimedTab(cdpEvent.TabID); tab != nil {
				tab.Dispatch(cdpEvent.SessionID, cdpEvent.Method, cdpEvent.Params)
			}
		case extproto.EventDetached:
			var detached extproto.DetachedEvent
			if event.DecodeData(&detached) != nil {
				continue
			}
			r.releaseClaim(detached.TabID, detached.Reason)
		case extproto.EventTabAssigned:
			var assigned extproto.TabAssignedEvent
			if event.DecodeData(&assigned) != nil {
				continue
			}
			if public, ok := r.publicID(browser, assigned.TabID); ok {
				if r.sink != nil {
					r.sink.TabAssigned(public, assigned.AppID)
				}
			}
		case extproto.EventTabOpened:
			var opened extproto.TabOpenedEvent
			if event.DecodeData(&opened) != nil {
				continue
			}
			public := r.assignChromeID(browser, opened.TabID, opened.URL)
			opener, _ := r.publicID(browser, opened.OpenerTabID)
			if r.sink != nil {
				r.sink.TabOpened(public, opener, opened.URL)
			}
		}
	}
}

func (r *Registry) disconnect(browser *Browser, client *extension.Client) {
	browser.mu.Lock()
	var claimed []int
	if browser.conn == client {
		browser.conn = nil
		browser.leftAt = time.Now()
		browser.urlsWhenLeft = make(map[int]string)
		for public, tab := range browser.tabs {
			browser.urlsWhenLeft[tab.chromeID] = tab.URL
			claimed = append(claimed, public)
		}
	}
	browser.mu.Unlock()
	// A claim belongs to the connection that made it; the next use on the
	// browser that comes back claims the tab again on the new connection.
	for _, public := range claimed {
		r.releaseClaimByPublic(public, "the browser disconnected")
	}
	if r.sink != nil {
		r.sink.Connection(false)
	}
}

func (r *Registry) applyState(browser *Browser, client *extension.Client, state extproto.State) {
	// r.mu guards the browser list, spare IDs and tombstones that public-ID
	// allocation reads; browser.mu guards this browser. Always r.mu first.
	r.mu.Lock()
	defer r.mu.Unlock()
	browser.mu.Lock()
	if browser.conn != client {
		browser.mu.Unlock()
		return
	}
	if state.Pairing == "paired" {
		browser.unconfirmed = false
	}
	if state.Label != nil {
		browser.label = state.Label
	}
	if state.LastFocusedAt != nil {
		browser.lastFocused = state.LastFocusedAt
	}
	seen := make(map[int]bool, len(state.Tabs))
	for _, reported := range state.Tabs {
		seen[reported.TabID] = true
		public, exists := browser.publicIDs[reported.TabID]
		if exists {
			if previous, ok := browser.urlsWhenLeft[reported.TabID]; ok && reported.URL != previous {
				// Chrome restarted and numbers tabs anew: the old public ID
				// is retired rather than naming another tab.
				delete(browser.publicIDs, reported.TabID)
				delete(browser.tabs, public)
				r.tombstones[public] = struct{}{}
				exists = false
			}
		}
		if !exists {
			public = r.allocatePublicID(browser, reported.TabID)
			browser.publicIDs[reported.TabID] = public
		}
		delete(browser.urlsWhenLeft, reported.TabID)
		browser.tabs[public] = &Tab{
			TabID:    public,
			Browser:  browser.ID,
			WindowID: reported.WindowID,
			URL:      reported.URL,
			Title:    reported.Title,
			Active:   reported.Active,
			Pinned:   reported.Pinned,
			AppID:    reported.AppID,
			Attached: reported.Attached,
			chromeID: reported.TabID,
		}
	}
	// Tabs the browser no longer reports are closed
	var closed []int
	for chromeID, public := range browser.publicIDs {
		if seen[chromeID] {
			continue
		}
		delete(browser.publicIDs, chromeID)
		delete(browser.tabs, public)
		r.tombstones[public] = struct{}{}
		closed = append(closed, public)
	}
	browser.mu.Unlock()
	for _, public := range closed {
		r.releaseClaimByPublic(public, "the tab closed")
	}
}

// allocatePublicID gives a tab Chrome's ID, unless another browser already
// uses it, when it gets a spare above 100000. Caller holds browser.mu.
func (r *Registry) allocatePublicID(browser *Browser, chromeID int) int {
	if !r.usedByOtherBrowser(browser, chromeID) {
		if _, tomb := r.tombstones[chromeID]; !tomb {
			return chromeID
		}
	}
	for {
		r.spareID++
		if !r.usedByOtherBrowser(browser, r.spareID) {
			return r.spareID
		}
	}
}

func (r *Registry) usedByOtherBrowser(mine *Browser, id int) bool {
	for _, other := range r.browsers {
		if other == mine {
			continue
		}
		other.mu.Lock()
		_, used := other.tabs[id]
		other.mu.Unlock()
		if used {
			return true
		}
	}
	return false
}

func (r *Registry) assignChromeID(browser *Browser, chromeID int, url string) int {
	browser.mu.Lock()
	defer browser.mu.Unlock()
	if public, ok := browser.publicIDs[chromeID]; ok {
		return public
	}
	public := r.allocatePublicID(browser, chromeID)
	browser.publicIDs[chromeID] = public
	browser.tabs[public] = &Tab{TabID: public, Browser: browser.ID, URL: url, chromeID: chromeID}
	return public
}

func (r *Registry) publicID(browser *Browser, chromeID int) (int, bool) {
	browser.mu.Lock()
	defer browser.mu.Unlock()
	public, ok := browser.publicIDs[chromeID]
	return public, ok
}

// --- Reads ---

// Browsers lists every browser known, in the order it first connected.
func (r *Registry) Browsers() []Summary {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	summaries := make([]Summary, 0, len(browsers))
	var focusedID string
	var focusedAt *int64
	for _, browser := range browsers {
		browser.mu.Lock()
		label := browser.DefaultLabel
		if browser.unconfirmed {
			label = browser.Name
		} else if browser.label != nil && *browser.label != "" {
			label = *browser.label
		}
		connected := browser.conn != nil && browser.conn.Open()
		summary := Summary{
			ID:          browser.ID,
			Label:       label,
			Connected:   connected,
			Tabs:        len(browser.tabs),
			LastFocused: browser.lastFocused,
			ConnectedAt: browser.connectedAt,
		}
		if connected {
			summary.Pairing = browser.conn.State().Pairing
			summary.Paused = browser.conn.State().Paused
			summary.Usable = summary.Pairing == "paired" && !summary.Paused
		}
		browser.mu.Unlock()
		if summary.Connected && summary.Usable && (focusedID == "" || later(summary.LastFocused, focusedAt)) {
			focusedID = summary.ID
			focusedAt = summary.LastFocused
		}
		summaries = append(summaries, summary)
	}
	if focusedID == "" {
		for _, summary := range summaries {
			if summary.Connected {
				focusedID = summary.ID
			}
		}
	}
	for i := range summaries {
		summaries[i].Default = summaries[i].ID == focusedID
	}
	return summaries
}

func (b *Browser) lastFocusedAt() *int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastFocused
}

func later(a, b *int64) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return *a > *b
}

// Tabs lists the tabs of the connected browsers.
func (r *Registry) Tabs() []Tab {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	var tabs []Tab
	for _, browser := range browsers {
		tabs = append(tabs, browser.Tabs()...)
	}
	sort.Slice(tabs, func(i, j int) bool { return tabs[i].TabID < tabs[j].TabID })
	return tabs
}

// FindTab returns the tab with this public ID, if it is known.
func (r *Registry) FindTab(tabID int) (*Tab, bool) {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	for _, browser := range browsers {
		browser.mu.Lock()
		tab, ok := browser.tabs[tabID]
		browser.mu.Unlock()
		if ok {
			return tab, true
		}
	}
	return nil, false
}

// TabClosed reports whether a tab is known to be closed: its browser is
// connected and paired and does not report it.
func (r *Registry) TabClosed(tabID int) bool {
	if _, ok := r.FindTab(tabID); ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, tomb := r.tombstones[tabID]; tomb {
		return true
	}
	for _, browser := range r.browsers {
		browser.mu.Lock()
		connected := browser.conn != nil && browser.conn.Open()
		paired := connected && browser.conn.Paired()
		browser.mu.Unlock()
		if connected && paired {
			return true
		}
	}
	return false
}

// Paused is Stop in every connected browser.
func (r *Registry) Paused() bool {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	connected := 0
	for _, browser := range browsers {
		if !browser.Connected() {
			continue
		}
		connected++
		if !browser.Paused() {
			return false
		}
	}
	return connected > 0
}

// Connected reports whether any browser is connected.
func (r *Registry) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, browser := range r.browsers {
		if browser.Connected() {
			return true
		}
	}
	return false
}

// Several reports whether more than one browser is connected, when outputs
// name them.
func (r *Registry) Several() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, browser := range r.browsers {
		if browser.Connected() {
			count++
		}
	}
	return count > 1
}

// UnavailableReason is the message for the model when no browser is usable,
// else nil.
func (r *Registry) UnavailableReason() string {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	if len(browsers) == 0 || !r.Connected() {
		return NoExtensionMessage
	}
	if r.Paused() {
		return PausedMessage
	}
	for _, browser := range browsers {
		if browser.Usable() {
			return ""
		}
	}
	for _, browser := range browsers {
		if browser.Connected() && !browser.Paired() {
			return PairingRequiredMessage(browser.PairingCode())
		}
	}
	return NoExtensionMessage
}

// BrowserByIDOrLabel finds a browser by its ID or label (ignoring case).
func (r *Registry) BrowserByIDOrLabel(name string) (*Browser, error) {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	for _, browser := range browsers {
		if browser.ID == name {
			return browser, nil
		}
	}
	var matches []*Browser
	for _, browser := range browsers {
		if equalFold(browser.Label(), name) {
			matches = append(matches, browser)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("There's no browser %q. Run `browser list`", name)
	}
	ids := make([]string, len(matches))
	for i, browser := range matches {
		ids[i] = browser.ID
	}
	return nil, fmt.Errorf("Several browsers are named %q: use an ID (%v) from `browser list`", name, ids)
}

// DefaultBrowser is where new tabs open: the usable browser focused last,
// else the one connected last.
func (r *Registry) DefaultBrowser() *Browser {
	summaries := r.Browsers()
	var chosen *Browser
	for _, summary := range summaries {
		if summary.Default {
			chosen = r.byID[summary.ID]
		}
	}
	if chosen == nil {
		r.mu.Lock()
		for _, browser := range r.browsers {
			if browser.Connected() {
				chosen = browser
			}
		}
		r.mu.Unlock()
	}
	return chosen
}

// --- Tab actions ---

// CreateTab opens a background tab. app groups it; browser "" uses the default.
func (r *Registry) CreateTab(ctx context.Context, url string, app extproto.AppInfo, browserID string) (int, error) {
	browser, err := r.browserFor(ctx, browserID)
	if err != nil {
		return 0, err
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return 0, err
	}
	params := extproto.CreateTabParams{App: app}
	if url != "" {
		params.URL = &url
	}
	result, err := conn.Request(ctx, extproto.MethodCreateTab, params, 30*time.Second)
	if err != nil {
		return 0, err
	}
	var created extproto.CreateTabResult
	if err := json.Unmarshal(result, &created); err != nil {
		return 0, err
	}
	return r.assignChromeID(browser, created.TabID, url), nil
}

// CloseTab closes a tab.
func (r *Registry) CloseTab(ctx context.Context, tabID int) error {
	browser, _, err := r.browserOfTab(tabID)
	if err != nil {
		return err
	}
	chromeID, ok := r.chromeID(browser, tabID)
	if !ok {
		return fmt.Errorf("There's no tab %d. Run `tab list`", tabID)
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return err
	}
	r.releaseClaimByPublic(tabID, "the tab closed")
	returnConn, err := conn.Request(ctx, extproto.MethodCloseTab, extproto.CloseTabParams{TabID: chromeID}, 30*time.Second)
	_ = returnConn
	return err
}

// ActivateTab makes a tab active in its window.
func (r *Registry) ActivateTab(ctx context.Context, tabID int) error {
	browser, _, err := r.browserOfTab(tabID)
	if err != nil {
		return err
	}
	chromeID, ok := r.chromeID(browser, tabID)
	if !ok {
		return fmt.Errorf("There's no tab %d. Run `tab list`", tabID)
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return err
	}
	_, err = conn.Request(ctx, extproto.MethodActivateTab, extproto.ActivateTabParams{TabID: chromeID}, 30*time.Second)
	return err
}

// GroupTab puts a tab in an app's group, or out of any with a nil app.
func (r *Registry) GroupTab(ctx context.Context, tabID int, app *extproto.AppInfo) error {
	browser, _, err := r.browserOfTab(tabID)
	if err != nil {
		return err
	}
	chromeID, ok := r.chromeID(browser, tabID)
	if !ok {
		return fmt.Errorf("There's no tab %d. Run `tab list`", tabID)
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return err
	}
	_, err = conn.Request(ctx, extproto.MethodGroupTab,
		extproto.GroupTabParams{TabID: chromeID, App: app}, 30*time.Second)
	return err
}

// NoteActivity reports which app acted last, for the badge.
func (r *Registry) NoteActivity(appID, browserID string) {
	browser := r.byID[browserID]
	if browser == nil {
		browser = r.DefaultBrowser()
	}
	if browser == nil {
		return
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return
	}
	_, _ = conn.Request(context.Background(), extproto.MethodActivity,
		extproto.ActivityParams{AppID: appID}, 5*time.Second)
}

func (r *Registry) browserOfTab(tabID int) (*Browser, int, error) {
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	for _, browser := range browsers {
		browser.mu.Lock()
		tab, ok := browser.tabs[tabID]
		browser.mu.Unlock()
		if ok {
			return browser, tab.chromeID, nil
		}
	}
	if _, tomb := r.tombstones[tabID]; tomb {
		return nil, 0, fmt.Errorf("Tab %d closed", tabID)
	}
	// A tab of a browser that is away fails with its label
	for _, browser := range browsers {
		if !browser.Connected() {
			return nil, 0, fmt.Errorf("%s disconnected from Browser MCP. It reconnects by itself while it's open; meanwhile use a tab of another browser (`tab list`, `browser list`)", browser.Label())
		}
	}
	return nil, 0, fmt.Errorf("There's no tab %d. Run `tab list`", tabID)
}

func (r *Registry) chromeID(browser *Browser, publicID int) (int, bool) {
	browser.mu.Lock()
	defer browser.mu.Unlock()
	tab, ok := browser.tabs[publicID]
	if !ok {
		return 0, false
	}
	return tab.chromeID, true
}

func (r *Registry) browserFor(ctx context.Context, browserID string) (*Browser, error) {
	if browserID != "" {
		browser := r.byID[browserID]
		if browser == nil {
			return nil, fmt.Errorf("There's no browser %q. Run `browser list`", browserID)
		}
		return browser, nil
	}
	if browser := r.DefaultBrowser(); browser != nil {
		return browser, nil
	}
	return nil, errors.New(NoExtensionMessage)
}

func (r *Registry) connOf(browser *Browser) (*extension.Client, error) {
	browser.mu.Lock()
	conn := browser.conn
	browser.mu.Unlock()
	if conn == nil || !conn.Open() {
		return nil, fmt.Errorf("%s disconnected from Browser MCP. It reconnects by itself while it's open", browser.Label())
	}
	return conn, nil
}

// --- Claims ---

// Page claims a tab: it attaches the debugger (if needed) and returns the CDP
// session. Idempotent.
func (r *Registry) Page(ctx context.Context, tabID int) (*cdp.Tab, error) {
	if tab := r.claimedTabByPublic(tabID); tab != nil && !tab.Closed() {
		return tab, nil
	}
	chromeID, browser, err := r.tabTarget(tabID)
	if err != nil {
		return nil, err
	}
	r.claimsMu.Lock()
	if existing := r.claims[tabID]; existing != nil {
		if existing.tab != nil && existing.tab.Closed() {
			// The debugger let it go; claim it again
			delete(r.claims, tabID)
		} else {
			r.claimsMu.Unlock()
			select {
			case <-existing.done:
				if existing.tab != nil && !existing.tab.Closed() {
					return existing.tab, existing.err
				}
				return r.Page(ctx, tabID)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	entry := &claim{done: make(chan struct{})}
	r.claims[tabID] = entry
	r.claimsMu.Unlock()

	// A failed claim is dropped so the next use tries again; a successful one
	// stays cached, because a re-attach would hand the engine a new tab and
	// lose the snapshot refs it holds for this one.
	fail := func(err error) (*cdp.Tab, error) {
		entry.err = err
		r.claimsMu.Lock()
		if r.claims[tabID] == entry {
			delete(r.claims, tabID)
		}
		r.claimsMu.Unlock()
		close(entry.done)
		return nil, err
	}
	conn, err := r.connOf(browser)
	if err != nil {
		return fail(err)
	}
	if !browser.Paired() {
		return fail(errors.New(PairingRequiredMessage(browser.PairingCode())))
	}
	tab, err := cdp.Open(ctx, conn, chromeID)
	if err != nil {
		return fail(err)
	}
	entry.tab = tab
	close(entry.done)
	return tab, nil
}

func (r *Registry) tabTarget(tabID int) (int, *Browser, error) {
	browser, chromeID, err := r.browserOfTab(tabID)
	return chromeID, browser, err
}

// Release lets a claimed tab go; the tab stays open.
func (r *Registry) Release(ctx context.Context, tabID int) error {
	r.claimsMu.Lock()
	entry := r.claims[tabID]
	delete(r.claims, tabID)
	r.claimsMu.Unlock()
	if entry != nil && entry.tab != nil {
		return entry.tab.Detach(ctx)
	}
	return nil
}

// IsClaimed reports whether the tab has a live claim.
func (r *Registry) IsClaimed(tabID int) bool {
	tab := r.claimedTabByPublic(tabID)
	return tab != nil && !tab.Closed()
}

func (r *Registry) claimedTabByPublic(publicID int) *cdp.Tab {
	r.claimsMu.Lock()
	defer r.claimsMu.Unlock()
	if entry := r.claims[publicID]; entry != nil && entry.tab != nil {
		return entry.tab
	}
	return nil
}

func (r *Registry) claimedTab(chromeID int) *cdp.Tab {
	r.claimsMu.Lock()
	defer r.claimsMu.Unlock()
	for _, entry := range r.claims {
		if entry.tab != nil && entry.tab.TabID == chromeID {
			return entry.tab
		}
	}
	return nil
}

func (r *Registry) releaseClaim(chromeID int, reason string) {
	r.claimsMu.Lock()
	var public int
	var tab *cdp.Tab
	for id, entry := range r.claims {
		if entry.tab != nil && entry.tab.TabID == chromeID {
			public = id
			tab = entry.tab
			delete(r.claims, id)
		}
	}
	r.claimsMu.Unlock()
	if tab == nil {
		return
	}
	tab.MarkDetached(reason)
	if r.sink != nil {
		r.sink.Released(public, reason)
	}
}

func (r *Registry) releaseClaimByPublic(publicID int, reason string) {
	r.claimsMu.Lock()
	entry := r.claims[publicID]
	delete(r.claims, publicID)
	r.claimsMu.Unlock()
	if entry == nil || entry.tab == nil {
		return
	}
	entry.tab.MarkDetached(reason)
	if r.sink != nil {
		r.sink.Released(publicID, reason)
	}
}

// ReleaseAll detaches every claimed tab (shutdown, session end).
func (r *Registry) ReleaseAll(ctx context.Context) {
	r.claimsMu.Lock()
	entries := make(map[int]*claim, len(r.claims))
	for id, entry := range r.claims {
		entries[id] = entry
	}
	r.claims = make(map[int]*claim)
	r.claimsMu.Unlock()
	for _, entry := range entries {
		if entry.tab != nil {
			_ = entry.tab.Detach(ctx)
		}
	}
}

// --- Readiness ---

// ready reports whether a call can go ahead: a usable browser, a browser
// genuinely waiting for Allow (its pairing code is known), or every browser
// paused. A browser that has only just connected reports `pending` until its
// paired state arrives, so this waits for that rather than racing it.
func (r *Registry) ready() bool {
	if r.Paused() {
		return true
	}
	r.mu.Lock()
	browsers := append([]*Browser{}, r.browsers...)
	r.mu.Unlock()
	for _, browser := range browsers {
		if browser.Usable() {
			return true
		}
	}
	for _, browser := range browsers {
		if browser.Connected() && !browser.Paired() && browser.PairingCode() != "" {
			return true
		}
	}
	return false
}

// WhenReady waits briefly for the browser state a call needs: right after the
// daemon started, after a browser disconnected, or while a just-connected
// browser proves its pairing.
func (r *Registry) WhenReady(ctx context.Context, wait time.Duration) {
	if r.ready() {
		return
	}
	// A browser that is connected but still proving its pairing gets a short
	// grace period; waiting for one that is away can take the full wait.
	if r.Connected() && wait > 2*time.Second {
		wait = 2 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		r.mu.Lock()
		waker := make(chan struct{})
		r.waiters[waker] = struct{}{}
		r.mu.Unlock()
		select {
		case <-waker:
		case <-timer.C:
			r.removeWaiter(waker)
			return
		case <-ctx.Done():
			r.removeWaiter(waker)
			return
		}
		if r.ready() {
			return
		}
	}
}

func (r *Registry) removeWaiter(waker chan struct{}) {
	r.mu.Lock()
	delete(r.waiters, waker)
	r.mu.Unlock()
}

func (r *Registry) wake() {
	r.mu.Lock()
	waiters := r.waiters
	r.waiters = make(map[chan struct{}]struct{})
	r.mu.Unlock()
	for waker := range waiters {
		close(waker)
	}
}

func (r *Registry) notifyState() {
	if r.sink != nil {
		r.sink.State()
	}
	r.wake()
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
