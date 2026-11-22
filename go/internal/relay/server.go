// Package relay is the front door's HTTP + WebSocket server. It enforces the
// same loopback, Host, token and Origin rules as the TypeScript daemon, then
// proxies /client and /extension to the engine worker without parsing frames.
package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/protocol"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/proxy"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/worker"
)

// Engine is the worker manager the relay talks to.
type Engine interface {
	Ensure(ctx context.Context) (*worker.Endpoint, error)
	Stop(ctx context.Context)
}

// Options configures the front door.
type Options struct {
	Token         string
	Version       string
	Engine        Engine
	Logger        *slog.Logger
	Home          string
	OnClientOpen  func()
	OnClientClose func()
	OnShutdown    func()
}

// Server is the front door's HTTP and WebSocket server.
type Server struct {
	http      *http.Server
	listener  net.Listener
	opts      Options
	mu        sync.Mutex
	conns     map[*websocket.Conn]struct{}
	extension map[string]struct{}
}

var extensionOrigin = regexp.MustCompile(`^chrome-extension://([a-p]{32})$`)

// New prepares the server on an already-bound listener.
func New(listener net.Listener, opts Options) *Server {
	server := &Server{
		listener:  listener,
		opts:      opts,
		conns:     make(map[*websocket.Conn]struct{}),
		extension: make(map[string]struct{}),
	}
	server.extension[protocol.ChromeWebStoreExtensionID] = struct{}{}
	server.extension[protocol.SelfBuiltExtensionID] = struct{}{}
	for _, id := range strings.Split(os.Getenv("BROWSER_MCP_EXTENSION_IDS"), ",") {
		id = strings.TrimSpace(id)
		if validExtensionID(id) {
			server.extension[id] = struct{}{}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathStatus, server.handleStatus)
	mux.HandleFunc(protocol.PathShutdown, server.handleShutdown)
	mux.HandleFunc(protocol.PathExtension, server.handleExtension)
	mux.HandleFunc(protocol.PathClient, server.handleClient)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	server.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server
}

// Serve blocks until the server closes.
func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server and closes every proxied connection.
func (s *Server) Close() error {
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close(websocket.StatusGoingAway, "")
	}
	s.conns = make(map[*websocket.Conn]struct{})
	s.mu.Unlock()
	return s.http.Close()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !allowedHost(r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); strings.HasPrefix(origin, "chrome-extension://") {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.Status{
		Product:     protocol.Product,
		Version:     s.opts.Version,
		Protocol:    protocol.Version,
		MinProtocol: protocol.MinVersion,
		PID:         os.Getpid(),
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !allowedHost(r.Host) || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("Origin") != "" || !tokenOK(r.URL.Query().Get("token"), s.opts.Token) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.opts.OnShutdown()
	}()
}

func (s *Server) handleExtension(w http.ResponseWriter, r *http.Request) {
	if !allowedHost(r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	if !s.allowedExtension(origin) {
		if s.opts.Logger != nil {
			s.opts.Logger.Warn("rejected extension origin", "origin", origin)
		}
		w.WriteHeader(http.StatusForbidden)
		return
	}
	client, err := accept(w, r)
	if err != nil {
		return
	}
	defer client.Close(websocket.StatusNormalClosure, "")

	endpoint, err := s.opts.Engine.Ensure(r.Context())
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "engine unavailable")
		return
	}
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	upstream, err := dial(r.Context(), endpoint.ExtensionURL(), header)
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "engine unavailable")
		return
	}
	defer upstream.Close(websocket.StatusNormalClosure, "")

	s.track(client)
	s.track(upstream)
	defer s.untrack(client)
	defer s.untrack(upstream)
	_ = proxy.Pipe(r.Context(), client, upstream)
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	if !allowedHost(r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("Origin") != "" || !tokenOK(r.URL.Query().Get("token"), s.opts.Token) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	client, err := accept(w, r)
	if err != nil {
		return
	}
	defer client.Close(websocket.StatusNormalClosure, "")

	endpoint, err := s.opts.Engine.Ensure(r.Context())
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "engine unavailable")
		return
	}
	upstream, err := dial(r.Context(), endpoint.ClientURL(), nil)
	if err != nil {
		_ = client.Close(websocket.StatusInternalError, "engine unavailable")
		return
	}
	defer upstream.Close(websocket.StatusNormalClosure, "")

	s.track(client)
	s.track(upstream)
	defer s.untrack(client)
	defer s.untrack(upstream)
	if s.opts.OnClientOpen != nil {
		s.opts.OnClientOpen()
	}
	defer func() {
		if s.opts.OnClientClose != nil {
			s.opts.OnClientClose()
		}
	}()

	// The thin process sends `hello` first. Read it here so extension IDs a
	// running client adds (BROWSER_MCP_EXTENSION_IDS in its hello) reach this
	// front door's allowlist, as they do in the Node daemon, then forward it.
	firstContext, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	typ, data, err := client.Read(firstContext)
	cancel()
	if err != nil {
		_ = client.Close(websocket.StatusPolicyViolation, "Expected hello")
		return
	}
	s.noteClientHello(data)
	if err := upstream.Write(r.Context(), typ, data); err != nil {
		_ = client.Close(websocket.StatusInternalError, "engine unavailable")
		return
	}

	_ = proxy.Pipe(r.Context(), client, upstream)
}

func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(protocol.MaxPayload)
	return conn, nil
}

func dial(ctx context.Context, url string, header http.Header) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(protocol.MaxPayload)
	return conn, nil
}

func (s *Server) track(conn *websocket.Conn) {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(conn *websocket.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *Server) noteClientHello(data []byte) {
	var hello struct {
		Type         string   `json:"type"`
		ExtensionIDs []string `json:"extensionIds"`
	}
	if json.Unmarshal(data, &hello) != nil || hello.Type != "hello" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range hello.ExtensionIDs {
		if validExtensionID(id) {
			s.extension[id] = struct{}{}
		}
	}
}

func (s *Server) allowedExtension(origin string) bool {
	match := extensionOrigin.FindStringSubmatch(origin)
	if match == nil {
		return false
	}
	id := match[1]
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.extension[id]; ok {
		return true
	}
	if s.opts.Home == "" {
		return false
	}
	for _, dev := range readDevExtensionIDs(config.DevExtensionIDsPath(s.opts.Home)) {
		s.extension[dev] = struct{}{}
	}
	_, ok := s.extension[id]
	return ok
}

func readDevExtensionIDs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if validExtensionID(line) {
			ids = append(ids, line)
		}
	}
	return ids
}

func validExtensionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, char := range id {
		if char < 'a' || char > 'p' {
			return false
		}
	}
	return true
}

func allowedHost(hostport string) bool {
	host := hostport
	if value, _, err := net.SplitHostPort(hostport); err == nil {
		host = value
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func tokenOK(got, want string) bool {
	if got == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
