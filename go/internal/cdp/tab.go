// Package cdp is the native engine's Chrome DevTools Protocol transport: one
// claimed tab, its attach/detach lifecycle, commands with their answers, child
// sessions (out-of-process iframes) and event dispatch. It speaks CDP over the
// extension's `cdp` request; there is no CDP id correlation because the
// extension's request already pairs each command with its answer.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extension"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
)

// CommandTimeout bounds any one CDP command. Playwright uses five minutes for
// the same reason: a Runtime.evaluate that awaits a promise until a page-side
// wait gives up can take that long.
const CommandTimeout = 5 * time.Minute

// Handler receives one CDP event.
type Handler func(sessionID string, params json.RawMessage)

// Tab is a claimed tab: the debugger is attached and commands reach its page.
type Tab struct {
	ext      *extension.Client
	TabID    int
	TargetID string
	Info     extproto.TargetInfo

	mu          sync.RWMutex
	handlers    map[string]map[int]Handler
	anyHandler  map[int]Handler
	nextHandler int

	sessions sync.Map // child session IDs

	done      chan struct{}
	closeOnce sync.Once
	reason    string
}

// Open attaches the debugger to the tab and returns its session.
func Open(ctx context.Context, ext *extension.Client, tabID int) (*Tab, error) {
	result, err := ext.Request(ctx, extproto.MethodAttach,
		extproto.AttachParams{TabID: tabID}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var attached extproto.AttachResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &attached); err != nil {
			return nil, fmt.Errorf("attach answer: %w", err)
		}
	}
	tab := &Tab{
		ext:        ext,
		TabID:      tabID,
		TargetID:   attached.TargetInfo.TargetID,
		Info:       attached.TargetInfo,
		handlers:   make(map[string]map[int]Handler),
		anyHandler: make(map[int]Handler),
		done:       make(chan struct{}),
	}
	// Out-of-process iframes: ask Chrome to attach them to this debugger
	// session and report them with a sessionId, so their events and nodes
	// reach us. Chrome answers with an error on targets that don't support
	// auto-attach; the tab works either way.
	_, _ = tab.Send(ctx, "Target.setAutoAttach", map[string]any{
		"autoAttach":             true,
		"waitForDebuggerOnStart": false,
		"flatten":                true,
	})
	return tab, nil
}

// Send runs one CDP command on the tab's main session.
func (t *Tab) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return t.SendOn(ctx, "", method, params)
}

// SendOn runs one CDP command on a child session ("" for the tab's own).
func (t *Tab) SendOn(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = encoded
	}
	return t.SendJSON(ctx, sessionID, method, raw)
}

// SendJSON is Send with params already encoded.
func (t *Tab) SendJSON(ctx context.Context, sessionID, method string, params json.RawMessage) (json.RawMessage, error) {
	select {
	case <-t.done:
		return nil, fmt.Errorf("the tab detached: %s", t.reason)
	default:
	}
	result, err := t.ext.Request(ctx, extproto.MethodCDP, extproto.CDPParams{
		TabID:     t.TabID,
		SessionID: sessionID,
		Method:    method,
		Params:    params,
	}, CommandTimeout)
	if err != nil && looksDetached(err.Error()) {
		// Chrome let the tab go (another extension's frame, Cancel, a
		// renderer swap); the next use claims it again.
		t.MarkDetached("detached")
	}
	return result, err
}

func looksDetached(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "isn't attached") ||
		strings.Contains(lower, "not attached") ||
		strings.Contains(lower, "detached while handling")
}

// Eval runs a JavaScript expression in the page and decodes its value into
// out. It uses Runtime.evaluate with returnByValue, so simple values arrive
// as JSON.
func (t *Tab) Eval(ctx context.Context, expression string, out any) error {
	result, err := t.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	if err != nil {
		return err
	}
	var reply struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return err
	}
	if reply.ExceptionDetails != nil {
		message := reply.ExceptionDetails.Exception.Description
		if message == "" {
			message = reply.ExceptionDetails.Text
		}
		return errors.New(firstLine(message))
	}
	if out == nil || len(reply.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(reply.Result.Value, out)
}

// On registers a handler for one CDP event, or for all events with method
// "*". The returned function unregisters it.
func (t *Tab) On(method string, handler Handler) func() {
	t.mu.Lock()
	id := t.nextHandler
	t.nextHandler++
	if method == "*" {
		t.anyHandler[id] = handler
	} else {
		if t.handlers[method] == nil {
			t.handlers[method] = make(map[int]Handler)
		}
		t.handlers[method][id] = handler
	}
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		delete(t.anyHandler, id)
		if handlers := t.handlers[method]; handlers != nil {
			delete(handlers, id)
			if len(handlers) == 0 {
				delete(t.handlers, method)
			}
		}
		t.mu.Unlock()
	}
}

// Dispatch delivers one event to its handlers. The browser registry owns the
// extension's single event stream and routes cdpEvent frames here.
func (t *Tab) Dispatch(sessionID, method string, params json.RawMessage) {
	switch method {
	case "Target.attachedToTarget":
		var event struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(params, &event) == nil && event.SessionID != "" {
			t.sessions.Store(event.SessionID, struct{}{})
		}
	case "Target.detachedFromTarget":
		var event struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(params, &event) == nil {
			t.sessions.Delete(event.SessionID)
		}
	}
	t.mu.RLock()
	var listeners []Handler
	for _, handler := range t.handlers[method] {
		listeners = append(listeners, handler)
	}
	for _, handler := range t.anyHandler {
		listeners = append(listeners, handler)
	}
	t.mu.RUnlock()
	for _, listener := range listeners {
		listener(sessionID, params)
	}
}

// MarkDetached ends the session after the extension reported a detach.
func (t *Tab) MarkDetached(reason string) {
	t.closeOnce.Do(func() {
		t.reason = reason
		close(t.done)
	})
}

// Detach lets the tab go; the tab stays open. A tab already detached is not
// an error.
func (t *Tab) Detach(ctx context.Context) error {
	t.MarkDetached("released")
	_, err := t.ext.Request(ctx, extproto.MethodDetach,
		extproto.DetachParams{TabID: t.TabID}, 30*time.Second)
	return err
}

// Done closes when the tab is detached or released.
func (t *Tab) Done() <-chan struct{} { return t.done }

// Closed reports whether the tab's session ended.
func (t *Tab) Closed() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// Reason is why the session ended, for the model.
func (t *Tab) Reason() string { return t.reason }

// nextCommandID is unused by the transport (the extension correlates
// commands) but exposed for tests that need distinct values.
var nextCommandID atomic.Int64

// NextCommandID returns a process-unique number, for tests and diagnostics.
func NextCommandID() int64 { return nextCommandID.Add(1) }

func firstLine(text string) string {
	for i, char := range text {
		if char == '\n' {
			return text[:i]
		}
	}
	return text
}
