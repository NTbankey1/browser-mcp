// Package protocol holds the loopback contract the Go front door shares with
// the thin processes and the extension. It mirrors the parts of
// @browsermcp/protocol that the front door needs; the extension protocol
// itself stays owned by the TypeScript package.
package protocol

const (
	// Product names the daemon in /browsermcp/status.
	Product = "browsermcp"

	// Version and MinVersion are the client/extension protocol versions the
	// TypeScript daemon speaks. The front door is transparent, so it reports
	// the same values.
	Version    = 1
	MinVersion = 1

	// MaxPayload is the WebSocket payload cap both sides use.
	MaxPayload = 256 << 20
)

// Ports are the fixed relay ports, tried in order unless --port is given.
var Ports = []int{9010, 9011, 9012}

const (
	PathStatus    = "/browsermcp/status"
	PathExtension = "/extension"
	PathCDP       = "/cdp"
	PathClient    = "/client"
	PathShutdown  = "/shutdown"
)

// Extension IDs the daemon accepts on /extension. The front door checks the
// Origin shape and these IDs, then the worker enforces its own allowlist and
// the dev-extension-ids file.
const (
	ChromeWebStoreExtensionID = "bjfgambnhccakkhmkepdoekmckoijdlc"
	SelfBuiltExtensionID      = "imlplcjlhgpajopdbpbngiadkcmdbgob"
)

// Status is the JSON served at /browsermcp/status.
type Status struct {
	Product     string `json:"product"`
	Version     string `json:"version"`
	Protocol    int    `json:"protocol"`
	MinProtocol int    `json:"minProtocol"`
	PID         int    `json:"pid"`
}
