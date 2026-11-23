package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
)

// BufferSize is how many console messages, errors and requests one tab keeps.
const BufferSize = 500

// ConsoleEntry is one console message or uncaught error.
type ConsoleEntry struct {
	Type     string
	Text     string
	Location string
	Seq      int
}

// RequestEntry is one network request and its response.
type RequestEntry struct {
	ID        int
	RequestID string
	URL       string
	Method    string
	Type      string
	Status    int
	Headers   map[string]string
	PostData  string
	Failure   string
	RespHead  map[string]string
}

// DialogInfo is an open JavaScript dialog.
type DialogInfo struct {
	Type    string
	Message string
}

// Label is the dialog as the model sees it.
func (d *DialogInfo) Label() string {
	if d == nil {
		return "The dialog"
	}
	return fmt.Sprintf("%s dialog %q", d.Type, Truncate(d.Message, 200))
}

// CdpState is a session's raw-CDP bookkeeping, cleaned up on release.
type CdpState struct {
	Enabled    map[string]bool
	Subscribed map[string]bool
	Events     []CdpEventEntry
	Used       map[string]bool
	Paused     bool
}

// CdpEventEntry is one buffered raw CDP event.
type CdpEventEntry struct {
	Method string
	Params json.RawMessage
}

// TabState follows one claimed tab. A released and re-claimed tab gets a new
// state, so the buffers always describe the current Page.
type TabState struct {
	TabID int
	Tab   *cdp.Tab

	mu         sync.Mutex
	refs       map[string]int
	console    []ConsoleEntry
	uncaught   []ConsoleEntry
	seq        int
	errorCount int
	lastError  string
	requests   []RequestEntry
	requestBy  map[string]*RequestEntry
	requestID  int
	dialog     *DialogInfo
	lastUsed   time.Time
	cdp        *CdpState
	unsubs     []func()
	closed     bool
}

func newTabState(tabID int, tab *cdp.Tab) *TabState {
	return &TabState{
		TabID:     tabID,
		Tab:       tab,
		refs:      map[string]int{},
		requestBy: map[string]*RequestEntry{},
		lastUsed:  time.Now(),
	}
}

// watch enables the domains the engine reads and records what they report.
func (s *TabState) watch() {
	s.unsubs = append(s.unsubs,
		s.Tab.On("Runtime.consoleAPICalled", s.onConsole),
		s.Tab.On("Runtime.exceptionThrown", s.onException),
		s.Tab.On("Network.requestWillBeSent", s.onRequest),
		s.Tab.On("Network.responseReceived", s.onResponse),
		s.Tab.On("Network.loadingFailed", s.onFailed),
		s.Tab.On("Page.javascriptDialogOpening", s.onDialogOpening),
		s.Tab.On("Page.javascriptDialogClosed", s.onDialogClosed),
		s.Tab.On("Page.frameNavigated", s.onFrameNavigated),
		s.Tab.On("Debugger.paused", func(string, json.RawMessage) { s.setPaused(true) }),
		s.Tab.On("Debugger.resumed", func(string, json.RawMessage) { s.setPaused(false) }),
	)
}

func (s *TabState) dispose() {
	s.mu.Lock()
	s.closed = true
	unsubs := s.unsubs
	s.unsubs = nil
	s.mu.Unlock()
	for _, unsub := range unsubs {
		unsub()
	}
}

func (s *TabState) onConsole(_ string, params json.RawMessage) {
	var event struct {
		Type string `json:"type"`
		Args []struct {
			Type        string `json:"type"`
			Value       any    `json:"value"`
			Description string `json:"description"`
			Preview     struct {
				Description string `json:"description"`
			} `json:"preview"`
		} `json:"args"`
		StackTrace struct {
			CallFrames []struct {
				URL        string `json:"url"`
				LineNumber int    `json:"lineNumber"`
			} `json:"callFrames"`
		} `json:"stackTrace"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	var parts []string
	for _, arg := range event.Args {
		switch {
		case arg.Value != nil:
			parts = append(parts, CompactJSON(arg.Value))
		case arg.Preview.Description != "":
			parts = append(parts, arg.Preview.Description)
		case arg.Description != "":
			parts = append(parts, arg.Description)
		default:
			parts = append(parts, arg.Type)
		}
	}
	location := ""
	if frames := event.StackTrace.CallFrames; len(frames) > 0 && frames[0].URL != "" {
		location = fmt.Sprintf("%s:%d", frames[0].URL, frames[0].LineNumber+1)
	}
	// Chrome logs the favicon's 404 on many sites; it is never the goal
	if strings.Contains(location, "/favicon.ico") {
		return
	}
	s.mu.Lock()
	s.seq++
	entry := ConsoleEntry{Type: event.Type, Text: strings.Join(parts, " "), Location: location, Seq: s.seq}
	s.console = push(s.console, entry)
	if event.Type == "error" {
		s.errorCount++
		s.lastError = firstLineOf(entry.Text)
	}
	s.mu.Unlock()
}

func (s *TabState) onException(_ string, params json.RawMessage) {
	var event struct {
		ExceptionDetails struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	text := event.ExceptionDetails.Exception.Description
	if text == "" {
		text = event.ExceptionDetails.Text
	}
	s.mu.Lock()
	s.seq++
	s.uncaught = push(s.uncaught, ConsoleEntry{Type: "uncaught", Text: text, Seq: s.seq})
	s.errorCount++
	s.lastError = firstLineOf(text)
	s.mu.Unlock()
}

func (s *TabState) onRequest(_ string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		Type      string `json:"type"`
		Request   struct {
			URL      string            `json:"url"`
			Method   string            `json:"method"`
			Headers  map[string]string `json:"headers"`
			PostData string            `json:"postData"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	s.mu.Lock()
	s.requestID++
	entry := &RequestEntry{
		ID:        s.requestID,
		RequestID: event.RequestID,
		URL:       event.Request.URL,
		Method:    event.Request.Method,
		Type:      event.Type,
		Headers:   event.Request.Headers,
		PostData:  event.Request.PostData,
	}
	s.requestBy[event.RequestID] = entry
	s.requests = append(s.requests, *entry)
	if len(s.requests) > BufferSize {
		s.requests = s.requests[len(s.requests)-BufferSize:]
	}
	s.mu.Unlock()
}

func (s *TabState) onResponse(_ string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		Response  struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
		} `json:"response"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	s.mu.Lock()
	entry := s.requestBy[event.RequestID]
	if entry != nil {
		entry.Status = event.Response.Status
		entry.RespHead = event.Response.Headers
		for i := range s.requests {
			if s.requests[i].ID == entry.ID {
				s.requests[i].Status = entry.Status
				s.requests[i].RespHead = entry.RespHead
			}
		}
	}
	s.mu.Unlock()
}

func (s *TabState) onFailed(_ string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	s.mu.Lock()
	if entry := s.requestBy[event.RequestID]; entry != nil {
		entry.Failure = event.ErrorText
		for i := range s.requests {
			if s.requests[i].ID == entry.ID {
				s.requests[i].Failure = entry.Failure
			}
		}
	}
	s.mu.Unlock()
}

func (s *TabState) onDialogOpening(_ string, params json.RawMessage) {
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(params, &event) != nil {
		return
	}
	s.mu.Lock()
	s.dialog = &DialogInfo{Type: event.Type, Message: event.Message}
	s.mu.Unlock()
}

func (s *TabState) onDialogClosed(string, json.RawMessage) {
	s.mu.Lock()
	s.dialog = nil
	s.mu.Unlock()
}

func (s *TabState) onFrameNavigated(_ string, params json.RawMessage) {
	var event struct {
		Frame struct {
			ParentID string `json:"parentId"`
		} `json:"frame"`
	}
	_ = json.Unmarshal(params, &event)
	if event.Frame.ParentID != "" {
		return
	}
	// Refs name nodes from the page that just went away
	s.mu.Lock()
	s.refs = map[string]int{}
	s.mu.Unlock()
}

func (s *TabState) setPaused(paused bool) {
	s.mu.Lock()
	if s.cdp != nil {
		s.cdp.Paused = paused
	}
	s.mu.Unlock()
}

// Dialog is the open dialog, if any.
func (s *TabState) Dialog() *DialogInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialog
}

// SetRefs replaces the tab's ref table from a snapshot.
func (s *TabState) SetRefs(refs map[string]int) {
	s.mu.Lock()
	s.refs = refs
	s.mu.Unlock()
}

// BackendNodeID resolves a ref, and reports whether it is from this page.
func (s *TabState) BackendNodeID(ref string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.refs[ref]
	return id, ok
}

// ErrorsSince reports console errors seen, so a step can note new ones.
func (s *TabState) ErrorsSince(before int) (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorCount - before, s.lastError
}

// ErrorCount is the number of console errors and uncaught errors seen.
func (s *TabState) ErrorCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorCount
}

// LastError is the newest error's first line.
func (s *TabState) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

// Console returns console messages and uncaught errors, newest last.
func (s *TabState) Console(errorsOnly bool) []ConsoleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ConsoleEntry
	for _, entry := range s.console {
		if !errorsOnly || entry.Type == "error" {
			entries = append(entries, entry)
		}
	}
	for _, entry := range s.uncaught {
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	return entries
}

// Requests returns the request log, newest last.
func (s *TabState) Requests() []RequestEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestEntry{}, s.requests...)
}

// Request finds one request by its printed ID.
func (s *TabState) Request(id int) (RequestEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.requests {
		if entry.ID == id {
			return entry, true
		}
	}
	return RequestEntry{}, false
}

// CdpState returns the agent's raw-CDP bookkeeping, creating it on first use.
func (s *TabState) CdpState() *CdpState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cdp == nil {
		s.cdp = &CdpState{
			Enabled:    map[string]bool{},
			Subscribed: map[string]bool{},
			Used:       map[string]bool{},
		}
	}
	return s.cdp
}

// Cdp buffers a subscribed event.
func (s *TabState) BufferCdp(method string, params json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cdp == nil {
		return
	}
	s.cdp.Events = append(s.cdp.Events, CdpEventEntry{Method: method, Params: params})
	if len(s.cdp.Events) > BufferSize {
		s.cdp.Events = s.cdp.Events[len(s.cdp.Events)-BufferSize:]
	}
}

func push[T any](buffer []T, item T) []T {
	buffer = append(buffer, item)
	if len(buffer) > BufferSize {
		buffer = buffer[len(buffer)-BufferSize:]
	}
	return buffer
}

func firstLineOf(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
