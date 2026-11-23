// Package extproto is the extension↔daemon wire format, mirroring
// @browsermcp/protocol. Every frame is one JSON object. This package is the
// Go source of truth for the native engine; the TypeScript package stays the
// source of truth for the extension's implementation, and the conformance
// tests decode the same fixtures on both sides.
package extproto

import (
	"encoding/json"
	"fmt"
	"time"
)

// ProtocolVersion is the newest extension protocol the daemon speaks, and
// MinProtocol the oldest it accepts. A mismatch closes with
// CloseUnsupportedProtocol and a reason naming the side to update.
const (
	ProtocolVersion = 1
	MinProtocol     = 1
)

// MaxBrowsers is how many Chrome profiles (browsers) one daemon connects at
// once; one more is refused with CloseAnotherBrowser.
const MaxBrowsers = 8

// Ping timing: the daemon pings every PingInterval and closes a connection
// that does not answer within PingTimeout.
const (
	PingInterval = 20 * time.Second
	PingTimeout  = 10 * time.Second
)

// Close codes.
const (
	CloseUnsupportedProtocol = 4001
	CloseAnotherBrowser      = 4002
	CloseShuttingDown        = 4003
)

// Close reasons for CloseUnsupportedProtocol.
const (
	UpdateExtension = "update-extension"
	UpdateServer    = "update-server"
)

// AppInfo is one AI app; sessions of the same app share its tab group.
type AppInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ExposedTab is a tab agents may use, as the extension reports it. TabID is
// Chrome's ID, which means something only within one browser.
type ExposedTab struct {
	TabID    int     `json:"tabId"`
	WindowID int     `json:"windowId"`
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Active   bool    `json:"active"`
	Pinned   bool    `json:"pinned"`
	AppID    *string `json:"appId,omitempty"`
	Attached bool    `json:"attached"`
}

// State is what the extension reports: its tabs, Stop, pairing and, once
// paired, the user's label and last focus.
type State struct {
	Tabs          []ExposedTab `json:"tabs"`
	Paused        bool         `json:"paused"`
	Pairing       string       `json:"pairing"`
	Label         *string      `json:"label,omitempty"`
	LastFocusedAt *int64       `json:"lastFocusedAt,omitempty"`
}

// Paired reports whether the user clicked Allow for this daemon's key.
func (s State) Paired() bool { return s.Pairing == "paired" }

// BrowserInfo describes the browser a connection comes from.
type BrowserInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	UserAgent string `json:"userAgent"`
}

// Hello is the extension's first frame.
type Hello struct {
	Type             string      `json:"type"`
	Protocol         int         `json:"protocol"`
	ExtensionVersion string      `json:"extensionVersion"`
	Browser          BrowserInfo `json:"browser"`
	BrowserID        string      `json:"browserId,omitempty"`
	Capabilities     []string    `json:"capabilities"`
	Challenge        string      `json:"challenge"`
	State            State       `json:"state"`
}

// Welcome is the daemon's answer: its key, the commitment to its nonce and
// the signature over Payload(challenge, port, commitment).
type Welcome struct {
	Type              string   `json:"type"`
	Protocol          int      `json:"protocol"`
	DaemonVersion     string   `json:"daemonVersion"`
	Capabilities      []string `json:"capabilities"`
	PublicKey         string   `json:"publicKey"`
	PairingCommitment string   `json:"pairingCommitment"`
	Signature         string   `json:"signature"`
}

// PairingNonce is the extension's nonce, sent only for a key it has not
// paired with, before the daemon reveals its own.
type PairingNonce struct {
	Type  string `json:"type"`
	Nonce string `json:"nonce"`
}

// PairingReveal is the daemon's nonce, behind Welcome's commitment.
type PairingReveal struct {
	Type  string `json:"type"`
	Nonce string `json:"nonce"`
}

// Ping is sent every PingInterval; the extension answers Pong with the ID.
type Ping struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

// Pong answers Ping.
type Pong struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

// Response answers a Request. Error is a message meant for the model.
type Response struct {
	Type   string          `json:"type"`
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// EventFrame is an unsolicited event.
type EventFrame struct {
	Type  string `json:"type"`
	Event Event  `json:"event"`
}

// Event is one typed event with its data.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Request methods.
const (
	MethodAttach      = "attach"
	MethodDetach      = "detach"
	MethodCDP         = "cdp"
	MethodCreateTab   = "createTab"
	MethodCloseTab    = "closeTab"
	MethodActivateTab = "activateTab"
	MethodGroupTab    = "groupTab"
	MethodActivity    = "activity"
)

// Request is a daemon→extension request.
type Request struct {
	Type   string `json:"type"`
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// AttachParams asks for the debugger on a tab.
type AttachParams struct {
	TabID int `json:"tabId"`
}

// AttachResult carries the tab's CDP target info.
type AttachResult struct {
	TargetInfo TargetInfo `json:"targetInfo"`
}

// TargetInfo is the parts of Target.TargetInfo the engine uses.
type TargetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Attached bool   `json:"attached"`
}

// CDPParams tunnels one CDP command to a tab or one of its child sessions.
type CDPParams struct {
	TabID     int             `json:"tabId"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// DetachParams detaches the debugger from a tab.
type DetachParams struct {
	TabID int `json:"tabId"`
}

// CreateTabParams opens a background tab in the app's group.
type CreateTabParams struct {
	URL *string `json:"url,omitempty"`
	App AppInfo `json:"app"`
}

// CreateTabResult is the new tab's Chrome ID.
type CreateTabResult struct {
	TabID int `json:"tabId"`
}

// CloseTabParams closes a tab.
type CloseTabParams struct {
	TabID int `json:"tabId"`
}

// ActivateTabParams focuses a tab's window and makes it active.
type ActivateTabParams struct {
	TabID int `json:"tabId"`
}

// GroupTabParams puts a tab in an app's group, or out of any group with a
// nil App.
type GroupTabParams struct {
	TabID int      `json:"tabId"`
	App   *AppInfo `json:"app"`
}

// ActivityParams reports which app acted last, for the badge.
type ActivityParams struct {
	AppID string `json:"appId"`
}

// Event types.
const (
	EventCDP         = "cdpEvent"
	EventDetached    = "detached"
	EventState       = "state"
	EventTabAssigned = "tabAssigned"
	EventTabOpened   = "tabOpened"
)

// CDPEvent is a CDP event from an attached tab or one of its child sessions.
type CDPEvent struct {
	TabID     int             `json:"tabId"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// DetachedEvent says the debugger let a tab go without the daemon asking.
type DetachedEvent struct {
	TabID  int    `json:"tabId"`
	Reason string `json:"reason"`
}

// TabAssignedEvent is the user dragging a tab into or out of an app's group.
type TabAssignedEvent struct {
	TabID int     `json:"tabId"`
	AppID *string `json:"appId"`
}

// TabOpenedEvent is a page opening a tab.
type TabOpenedEvent struct {
	TabID       int    `json:"tabId"`
	OpenerTabID int    `json:"openerTabId"`
	URL         string `json:"url"`
}

// DecodeEventData unmarshals an event's data into v.
func (e Event) DecodeData(v any) error {
	if len(e.Data) == 0 {
		return fmt.Errorf("event %q has no data", e.Type)
	}
	return json.Unmarshal(e.Data, v)
}

// Encode marshals a frame. The engine never builds JSON by hand, so one
// encoding path keeps every frame small and valid.
func Encode(v any) ([]byte, error) { return json.Marshal(v) }
