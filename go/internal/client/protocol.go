// Package client is the native daemon's side of `/client`: the hello/welcome
// handshake, tools/list and tools/call from a thin process, and the reverse
// readFile/writeFile requests the engine makes for uploads, screenshots, PDFs
// and downloads.
package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/engine"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
)

// Version is CLIENT_PROTOCOL_VERSION; a client newer than this closes with
// 4001 update-server so the thin process replaces the daemon.
const Version = 1

// Defaults matching the TypeScript daemon.
const (
	HelloTimeout    = 10 * time.Second
	RPCTimeout      = 30 * time.Second
	ToolCallTimeout = 5 * time.Minute
)

// Engine is what the client protocol needs from the command engine.
type Engine interface {
	Tools(sessionID string) []engine.Tool
	OpenSession(id string, app extproto.AppInfo, capabilities []string, io engine.SessionIO)
	CloseSession(ctx context.Context, id string)
	CallTool(ctx context.Context, sessionID, name string, args map[string]any) (engine.CallToolResult, error)
}

// Hello is the thin process's first frame.
type Hello struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Client   struct {
		Name    string `json:"name"`
		Title   string `json:"title,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"client"`
	Capabilities []string `json:"capabilities"`
	PID          int      `json:"pid"`
	ExtensionIDs []string `json:"extensionIds,omitempty"`
}

// Options configure a session.
type Options struct {
	Version string
	Log     *slog.Logger
}

// Accept serves one thin process until its connection closes. helloData is
// the first frame, which the relay reads to learn the client's extension IDs.
func Accept(ctx context.Context, ws *websocket.Conn, helloData []byte, eng Engine, options Options) error {
	var hello Hello
	if err := json.Unmarshal(helloData, &hello); err != nil || hello.Type != "hello" {
		_ = ws.Close(websocket.StatusPolicyViolation, "Expected hello")
		return errors.New("the thin process didn't send hello")
	}
	if hello.Protocol > Version {
		_ = ws.Close(websocket.StatusCode(4001), "update-server")
		return fmt.Errorf("client protocol %d is newer than %d", hello.Protocol, Version)
	}
	sessionID := newSessionID()
	welcome := map[string]any{
		"type": "welcome", "protocol": Version, "version": options.Version, "sessionId": sessionID,
	}
	data, _ := json.Marshal(welcome)
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := ws.Write(writeCtx, websocket.MessageText, data)
	cancel()
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "")
		return err
	}

	peer := newPeer(ws, options.Log)
	app := appFromClient(hello.Client.Name, hello.Client.Title)
	capabilities := hello.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	eng.OpenSession(sessionID, app, capabilities, &sessionIO{peer: peer})
	if options.Log != nil {
		options.Log.Info("session opened", "session", sessionID, "app", app.Name,
			"client", hello.Client.Name, "pid", hello.PID)
	}

	peer.handlers["tools/list"] = func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{"tools": eng.Tools(sessionID)}, nil
	}
	peer.handlers["tools/call"] = func(ctx context.Context, params json.RawMessage) (any, error) {
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, err
		}
		callCtx, cancel := context.WithTimeout(ctx, ToolCallTimeout)
		defer cancel()
		result, err := eng.CallTool(callCtx, sessionID, call.Name, call.Arguments)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	peer.readLoop()
	eng.CloseSession(context.Background(), sessionID)
	if options.Log != nil {
		options.Log.Info("session closed", "session", sessionID)
	}
	return nil
}

// sessionIO answers the engine's file requests over the client connection.
type sessionIO struct{ peer *peer }

func (s *sessionIO) ReadFile(ctx context.Context, path string) (string, string, []byte, error) {
	result, err := s.peer.request(ctx, "readFile", map[string]any{"path": path})
	if err != nil {
		return "", "", nil, err
	}
	var file struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
		Base64   string `json:"base64"`
	}
	if err := json.Unmarshal(result, &file); err != nil {
		return "", "", nil, err
	}
	data, err := base64.StdEncoding.DecodeString(file.Base64)
	return file.Name, file.MimeType, data, err
}

func (s *sessionIO) WriteFile(ctx context.Context, path string, data []byte, exclusive bool) (string, error) {
	params := map[string]any{"path": path, "base64": base64.StdEncoding.EncodeToString(data)}
	if exclusive {
		params["exclusive"] = true
	}
	result, err := s.peer.request(ctx, "writeFile", params)
	if err != nil {
		return "", err
	}
	var written struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(result, &written); err != nil {
		return "", err
	}
	return written.Path, nil
}

// peer runs JSON-RPC-style requests in both directions on one socket.
type peer struct {
	ws       *websocket.Conn
	log      *slog.Logger
	writeMu  sync.Mutex
	nextID   atomic.Int64
	mu       sync.Mutex
	pending  map[int64]chan rpcResponse
	handlers map[string]func(context.Context, json.RawMessage) (any, error)
	closed   chan struct{}
	once     sync.Once
}

type rpcResponse struct {
	Result json.RawMessage
	Error  string
}

func newPeer(ws *websocket.Conn, log *slog.Logger) *peer {
	return &peer{
		ws:       ws,
		log:      log,
		pending:  map[int64]chan rpcResponse{},
		handlers: map[string]func(context.Context, json.RawMessage) (any, error){},
		closed:   make(chan struct{}),
	}
}

func (p *peer) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	select {
	case <-p.closed:
		return nil, errors.New("Connection closed")
	default:
	}
	id := p.nextID.Add(1)
	answer := make(chan rpcResponse, 1)
	p.mu.Lock()
	p.pending[id] = answer
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	frame, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := p.write(ctx, frame); err != nil {
		return nil, err
	}
	timer := time.NewTimer(RPCTimeout)
	defer timer.Stop()
	select {
	case response := <-answer:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Result, nil
	case <-timer.C:
		return nil, fmt.Errorf("No answer to %s within 30 s", method)
	case <-p.closed:
		return nil, errors.New("Connection closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *peer) write(ctx context.Context, frame []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.ws.Write(writeCtx, websocket.MessageText, frame)
}

func (p *peer) readLoop() {
	defer p.shutdown()
	for {
		_, data, err := p.ws.Read(context.Background())
		if err != nil {
			return
		}
		var frame struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &frame) != nil || frame.ID == nil {
			continue
		}
		if frame.Method != "" {
			go p.answer(*frame.ID, frame.Method, frame.Params)
			continue
		}
		p.mu.Lock()
		answer := p.pending[*frame.ID]
		delete(p.pending, *frame.ID)
		p.mu.Unlock()
		if answer == nil {
			continue
		}
		response := rpcResponse{Result: frame.Result}
		if frame.Error != nil {
			response.Error = frame.Error.Message
		}
		answer <- response
	}
}

func (p *peer) answer(id int64, method string, params json.RawMessage) {
	var frame []byte
	handler := p.handlers[method]
	if handler == nil {
		frame, _ = json.Marshal(map[string]any{"id": id, "error": map[string]string{"message": "Unknown method: " + method}})
	} else {
		result, err := handler(context.Background(), params)
		if err != nil {
			frame, _ = json.Marshal(map[string]any{"id": id, "error": map[string]string{"message": err.Error()}})
		} else {
			frame, _ = json.Marshal(map[string]any{"id": id, "result": result})
		}
	}
	_ = p.write(context.Background(), frame)
}

func (p *peer) shutdown() {
	p.once.Do(func() {
		close(p.closed)
		p.mu.Lock()
		pending := p.pending
		p.pending = map[int64]chan rpcResponse{}
		p.mu.Unlock()
		for _, answer := range pending {
			answer <- rpcResponse{Error: "Connection closed"}
		}
	})
}

// --- App naming ---

var knownApps = map[string]string{
	"claude-code":           "Claude Code",
	"claude-ai":             "Claude",
	"cursor-vscode":         "Cursor",
	"codex-mcp-client":      "Codex",
	"Visual Studio Code":    "VS Code",
	"windsurf-client":       "Windsurf",
	"gemini-cli-mcp-client": "Gemini CLI",
	"zed":                   "Zed",
}

// appFromClient names the AI app: sessions of the same app share a tab group.
func appFromClient(name, title string) extproto.AppInfo {
	raw := strings.TrimSpace(name)
	if raw == "" {
		raw = "AI app"
	}
	display := knownApps[raw]
	if display == "" && strings.TrimSpace(title) != "" {
		display = strings.TrimSpace(title)
	}
	if display == "" {
		display = strings.ToUpper(raw[:1]) + raw[1:]
	}
	id := slug(raw)
	if id == "" {
		id = "app"
	}
	return extproto.AppInfo{ID: id, Name: display}
}

func slug(text string) string {
	var builder strings.Builder
	dash := false
	for _, char := range strings.ToLower(text) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
			dash = false
		} else if !dash && builder.Len() > 0 {
			builder.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func newSessionID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:4]) + "-" + hex.EncodeToString(raw[4:6]) + "-" +
		hex.EncodeToString(raw[6:8]) + "-" + hex.EncodeToString(raw[8:10]) + "-" +
		hex.EncodeToString(raw[10:])
}
