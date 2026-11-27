// Package extension is the daemon's side of one extension connection on
// /extension: the hello/welcome handshake, the pairing nonce exchange,
// requests with a timeout, events, and the ping that closes a connection whose
// service worker stopped answering.
package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/brand"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extproto"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/pairing"
)

// Errors callers branch on.
var (
	ErrClosed      = errors.New("the browser disconnected from " + brand.Name)
	ErrNotPaired   = errors.New("the extension is waiting for Allow")
	ErrUnavailable = errors.New("the browser didn't answer in time")
)

// Options configure one accepted connection.
type Options struct {
	Identity *pairing.Identity
	Version  string
	// Port is the port the extension dialed, which the daemon signs into the
	// welcome; a front door passes the public port, not its internal one.
	Port int
	Log  *slog.Logger

	HelloTimeout   time.Duration
	RequestTimeout time.Duration
	PingInterval   time.Duration
	PingTimeout    time.Duration
}

func (o *Options) defaults() {
	if o.HelloTimeout == 0 {
		o.HelloTimeout = 10 * time.Second
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 30 * time.Second
	}
	if o.PingInterval == 0 {
		o.PingInterval = extproto.PingInterval
	}
	if o.PingTimeout == 0 {
		o.PingTimeout = extproto.PingTimeout
	}
}

// Client is an accepted, welcomed extension connection.
type Client struct {
	hello       extproto.Hello
	opts        Options
	ws          *websocket.Conn
	daemonNonce string

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan extproto.Response
	pongs   map[int64]chan struct{}

	stateMu sync.RWMutex
	state   extproto.State

	pairingMu   sync.RWMutex
	pairingCode string

	events    chan extproto.Event
	closed    chan struct{}
	closeOnce sync.Once
	closeCode websocket.StatusCode
	closeText string
	onClose   []func(code int, reason string)
}

// Accept reads the hello, answers welcome and starts the connection. The
// caller owns ws and must not read from it afterwards.
func Accept(ctx context.Context, ws *websocket.Conn, opts Options) (*Client, error) {
	opts.defaults()
	if opts.Identity == nil {
		return nil, errors.New("accept extension: no pairing identity")
	}
	helloCtx, cancel := context.WithTimeout(ctx, opts.HelloTimeout)
	defer cancel()
	_, data, err := ws.Read(helloCtx)
	if err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "Expected hello")
		return nil, fmt.Errorf("read hello: %w", err)
	}
	var hello extproto.Hello
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" || hello.Challenge == "" {
		_ = ws.Close(websocket.StatusPolicyViolation, "Expected hello")
		return nil, errors.New("the extension didn't send hello")
	}
	if hello.Protocol < extproto.MinProtocol {
		_ = ws.Close(websocket.StatusCode(extproto.CloseUnsupportedProtocol), extproto.UpdateExtension)
		return nil, fmt.Errorf("extension protocol %d is no longer supported", hello.Protocol)
	}
	if hello.State.Tabs == nil {
		hello.State.Tabs = []extproto.ExposedTab{}
	}

	daemonNonce, err := pairing.NewNonce()
	if err != nil {
		return nil, err
	}
	commitment, err := pairing.Commitment(daemonNonce)
	if err != nil {
		return nil, err
	}
	signature, err := opts.Identity.Sign(pairing.Payload(hello.Challenge, opts.Port, commitment))
	if err != nil {
		return nil, err
	}
	welcome := extproto.Welcome{
		Type:              "welcome",
		Protocol:          extproto.ProtocolVersion,
		DaemonVersion:     opts.Version,
		Capabilities:      []string{},
		PublicKey:         opts.Identity.PublicKey,
		PairingCommitment: commitment,
		Signature:         signature,
	}
	client := &Client{
		hello:       hello,
		opts:        opts,
		ws:          ws,
		daemonNonce: daemonNonce,
		pending:     make(map[int64]chan extproto.Response),
		pongs:       make(map[int64]chan struct{}),
		state:       hello.State,
		events:      make(chan extproto.Event, 256),
		closed:      make(chan struct{}),
	}
	if err := client.write(ctx, welcome); err != nil {
		_ = ws.Close(websocket.StatusInternalError, "")
		return nil, err
	}
	go client.readLoop()
	go client.pingLoop()
	return client, nil
}

// Hello is the extension's hello frame.
func (c *Client) Hello() extproto.Hello { return c.hello }

// State is the newest state the extension reported.
func (c *Client) State() extproto.State {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

// Paired reports whether the user clicked Allow for this daemon's key.
func (c *Client) Paired() bool { return c.State().Paired() }

// PairingCode is the six-digit code the popup shows, once the extension sent
// its nonce; empty while the key is already paired or before the nonce came.
func (c *Client) PairingCode() string {
	c.pairingMu.RLock()
	defer c.pairingMu.RUnlock()
	return c.pairingCode
}

// Events is the connection's event stream. One consumer is expected (the
// browser registry, which fans out). It stops carrying events when Done closes.
func (c *Client) Events() <-chan extproto.Event { return c.events }

// Done closes when the connection closes.
func (c *Client) Done() <-chan struct{} { return c.closed }

// Open reports whether the connection is still up.
func (c *Client) Open() bool {
	select {
	case <-c.closed:
		return false
	default:
		return true
	}
}

// OnClose registers a listener run once when the connection closes.
func (c *Client) OnClose(listener func(code int, reason string)) {
	c.pendMu.Lock()
	closed := !c.Open()
	if !closed {
		c.onClose = append(c.onClose, listener)
	}
	c.pendMu.Unlock()
	if closed {
		listener(int(c.closeCode), c.closeText)
	}
}

// Request sends one request and waits for its answer. A CDP request may take
// minutes; everything else waits RequestTimeout by default.
func (c *Client) Request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if !c.Open() {
		return nil, ErrClosed
	}
	if timeout <= 0 {
		timeout = c.opts.RequestTimeout
	}
	id := c.nextID.Add(1)
	answer := make(chan extproto.Response, 1)
	c.pendMu.Lock()
	c.pending[id] = answer
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()
	if err := c.write(ctx, extproto.Request{Type: "request", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-answer:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Result, nil
	case <-timer.C:
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, describe(method, params))
	case <-c.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ping sends a ping and reports whether the extension answered in time.
func (c *Client) Ping(ctx context.Context, timeout time.Duration) bool {
	if !c.Open() {
		return false
	}
	id := c.nextID.Add(1)
	answer := make(chan struct{}, 1)
	c.pendMu.Lock()
	c.pongs[id] = answer
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pongs, id)
		c.pendMu.Unlock()
	}()
	if err := c.write(ctx, extproto.Ping{Type: "ping", ID: id}); err != nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-answer:
		return true
	case <-timer.C:
		return false
	case <-c.closed:
		return false
	case <-ctx.Done():
		return false
	}
}

// Close closes the connection with a code.
func (c *Client) Close(code int, reason string) {
	_ = c.ws.Close(websocket.StatusCode(code), reason)
	c.shutdown(websocket.StatusCode(code), reason)
}

func (c *Client) write(ctx context.Context, frame any) error {
	data, err := extproto.Encode(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(writeCtx, websocket.MessageText, data)
}

func (c *Client) readLoop() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			status := websocket.CloseStatus(err)
			if status == -1 {
				status = websocket.StatusAbnormalClosure
			}
			c.shutdown(status, "")
			return
		}
		c.onMessage(data)
	}
}

func (c *Client) onMessage(data []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &head) != nil {
		if c.opts.Log != nil {
			c.opts.Log.Warn("extension sent invalid JSON")
		}
		return
	}
	switch head.Type {
	case "response":
		var response extproto.Response
		if json.Unmarshal(data, &response) != nil {
			return
		}
		c.pendMu.Lock()
		answer := c.pending[response.ID]
		delete(c.pending, response.ID)
		c.pendMu.Unlock()
		if answer != nil {
			answer <- response
		}
	case "pong":
		var pong extproto.Pong
		if json.Unmarshal(data, &pong) != nil {
			return
		}
		c.pendMu.Lock()
		answer := c.pongs[pong.ID]
		delete(c.pongs, pong.ID)
		c.pendMu.Unlock()
		if answer != nil {
			close(answer)
		}
	case "event":
		var frame extproto.EventFrame
		if json.Unmarshal(data, &frame) != nil {
			return
		}
		if frame.Event.Type == extproto.EventState {
			var state extproto.State
			if frame.Event.DecodeData(&state) == nil {
				if state.Tabs == nil {
					state.Tabs = []extproto.ExposedTab{}
				}
				c.stateMu.Lock()
				c.state = state
				c.stateMu.Unlock()
			}
		}
		select {
		case c.events <- frame.Event:
		case <-c.closed:
		}
	case "pairingNonce":
		var nonce extproto.PairingNonce
		if json.Unmarshal(data, &nonce) != nil || nonce.Nonce == "" {
			c.Close(int(websocket.StatusPolicyViolation), "Unexpected pairingNonce")
			return
		}
		c.onPairingNonce(nonce.Nonce)
	}
}

func (c *Client) onPairingNonce(extensionNonce string) {
	reveal := extproto.PairingReveal{Type: "pairingReveal", Nonce: c.daemonNonce}
	if err := c.write(context.Background(), reveal); err != nil {
		return
	}
	code := pairing.Code(c.opts.Identity.PublicKey, extensionNonce, c.daemonNonce)
	c.pairingMu.Lock()
	c.pairingCode = code
	c.pairingMu.Unlock()
}

func (c *Client) pingLoop() {
	ticker := time.NewTicker(c.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.opts.PingTimeout)
			alive := c.Ping(ctx, c.opts.PingTimeout)
			cancel()
			if !alive {
				if c.opts.Log != nil {
					c.opts.Log.Warn("the extension stopped answering pings; closing")
				}
				_ = c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				c.shutdown(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func (c *Client) shutdown(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeText = reason
		close(c.closed)
		c.pendMu.Lock()
		for id, answer := range c.pending {
			delete(c.pending, id)
			answer <- extproto.Response{Type: "response", ID: id, Error: ErrClosed.Error()}
		}
		for id, answer := range c.pongs {
			delete(c.pongs, id)
			close(answer)
		}
		listeners := append([]func(int, string){}, c.onClose...)
		c.onClose = nil
		c.pendMu.Unlock()
		for _, listener := range listeners {
			listener(int(code), reason)
		}
	})
}

// describe names what timed out in the model's terms, as the TypeScript
// daemon does ("The browser didn't answer Input.dispatchMouseEvent in time").
func describe(method string, params any) string {
	if method == extproto.MethodCDP {
		if cdp, ok := params.(extproto.CDPParams); ok && cdp.Method != "" {
			return cdp.Method
		}
	}
	return method
}
