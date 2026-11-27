package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/daemon"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/pairing"
)

const selfBuiltExtensionID = "imlplcjlhgpajopdbpbngiadkcmdbgob"

func freeNativePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func startNative(t *testing.T) (*daemon.Daemon, string, int) {
	t.Helper()
	home := t.TempDir()
	port := freeNativePort(t)
	instance, err := daemon.Start(config.Config{
		Home: home, Port: port, Version: "test", IdleExit: time.Minute, Native: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		instance.Stop("test")
		select {
		case <-instance.Stopped():
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		t.Fatalf("daemon.json: %v %v", info, err)
	}
	return instance, home, info.Port
}

// --- fake extension ---

type fakeExtension struct {
	t         *testing.T
	publicKey string
	ws        *websocket.Conn
	writeMu   sync.Mutex
	url       string
	paired    bool
	frames    chan json.RawMessage
	// The page the tests describe: what `wait` and its timeouts read back.
	pageMu     sync.Mutex
	bodyText   string
	htmlText   string
	readyState string
}

// setPage describes the page the fake extension serves.
func (f *fakeExtension) setPage(bodyText, htmlText, readyState string) {
	f.pageMu.Lock()
	defer f.pageMu.Unlock()
	f.bodyText, f.htmlText, f.readyState = bodyText, htmlText, readyState
}

// probeReply answers the engine's wait probe (`textProbe` and `pageState`)
// from the page the test describes.
func (f *fakeExtension) probeReply(expression string) any {
	needle := ""
	if match := regexp.MustCompile(`const needle = ("(?:[^"\\]|\\.)*")`).FindStringSubmatch(expression); match != nil {
		_ = json.Unmarshal([]byte(match[1]), &needle)
	}
	f.pageMu.Lock()
	text, html, readyState := f.bodyText, f.htmlText, f.readyState
	f.pageMu.Unlock()
	return valueReply(map[string]any{
		"found":      strings.Contains(text, needle),
		"caseFold":   strings.Contains(strings.ToLower(text), strings.ToLower(needle)),
		"inHtml":     strings.Contains(html, needle),
		"readyState": readyState,
		"url":        f.url,
		"sample":     strings.Join(strings.Fields(text), " "),
	})
}

func connectExtension(t *testing.T, port int, pair bool) *fakeExtension {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Origin", "chrome-extension://"+selfBuiltExtensionID)
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/extension", port), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	fake := &fakeExtension{t: t, ws: ws, url: "about:blank", frames: make(chan json.RawMessage, 16),
		bodyText: "Hello page", readyState: "complete"}

	challenge := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	hello := map[string]any{
		"type": "hello", "protocol": 1, "extensionVersion": "test",
		"browser":      map[string]any{"name": "Chrome", "version": "1", "userAgent": "test-agent"},
		"browserId":    "abcdefghijklmnopqrstuv",
		"capabilities": []string{},
		"challenge":    challenge,
		"state":        map[string]any{"tabs": []any{}, "paused": false, "pairing": "pending"},
	}
	fake.write(hello)
	welcome := fake.read()
	if welcome["type"] != "welcome" {
		t.Fatalf("welcome: %v", welcome)
	}
	publicKey, _ := welcome["publicKey"].(string)
	fake.publicKey = publicKey
	commitment, _ := welcome["pairingCommitment"].(string)
	signature, _ := welcome["signature"].(string)
	ok, err := pairing.Verify(publicKey, pairing.Payload(challenge, port, commitment), signature)
	if err != nil || !ok {
		t.Fatalf("welcome signature: ok=%v err=%v", ok, err)
	}
	if pair {
		extensionNonce := base64.StdEncoding.EncodeToString([]byte("extension-nonce-0123456789abcdef"))
		fake.write(map[string]any{"type": "pairingNonce", "nonce": extensionNonce})
		reveal := fake.read()
		if reveal["type"] != "pairingReveal" {
			t.Fatalf("reveal: %v", reveal)
		}
		daemonNonce, _ := reveal["nonce"].(string)
		computed, err := pairing.Commitment(daemonNonce)
		if err != nil || computed != commitment {
			t.Fatalf("reveal does not match the commitment: %v %v", err, computed)
		}
		if code := pairing.Code(publicKey, extensionNonce, daemonNonce); len(code) != 7 {
			t.Fatalf("pairing code %q", code)
		}
		fake.paired = true
	}
	go fake.serve()
	return fake
}

func (f *fakeExtension) write(frame any) {
	f.t.Helper()
	data, _ := json.Marshal(frame)
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	// A write can race the test's cleanup closing the socket; that is not a
	// test failure (assertions check what the engine reported).
	_ = f.ws.Write(context.Background(), websocket.MessageText, data)
}

func (f *fakeExtension) read() map[string]any {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := f.ws.Read(ctx)
	if err != nil {
		f.t.Fatalf("extension read: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		f.t.Fatalf("extension frame: %v", err)
	}
	return frame
}

func (f *fakeExtension) sendState(tabs []map[string]any, pairing string) {
	f.write(map[string]any{"type": "event", "event": map[string]any{
		"type": "state",
		"data": map[string]any{"tabs": tabs, "paused": false, "pairing": pairing},
	}})
}

func (f *fakeExtension) serve() {
	for {
		// No per-read deadline: coder/websocket closes the whole connection
		// when a read's context expires.
		_, data, err := f.ws.Read(context.Background())
		if err != nil {
			return
		}
		var frame struct {
			Type   string          `json:"type"`
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &frame) != nil || frame.Type != "request" {
			continue
		}
		result, failure := f.answer(frame.Method, frame.Params)
		response := map[string]any{"type": "response", "id": frame.ID}
		if failure != "" {
			response["error"] = failure
		} else {
			response["result"] = result
		}
		f.write(response)
	}
}

func (f *fakeExtension) answer(method string, params json.RawMessage) (any, string) {
	switch method {
	case "attach":
		return map[string]any{"targetInfo": map[string]any{"targetId": "T1", "type": "page", "url": f.url, "title": ""}}, ""
	case "detach", "closeTab", "activateTab", "groupTab", "activity":
		return map[string]any{}, ""
	case "createTab":
		return map[string]any{"tabId": 2}, ""
	case "cdp":
		var cdp struct {
			TabID  int             `json:"tabId"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(params, &cdp); err != nil {
			return nil, err.Error()
		}
		return f.cdp(cdp.TabID, cdp.Method, cdp.Params)
	}
	return nil, "unknown method " + method
}

// event pushes one CDP event to the daemon, as the extension does.
func (f *fakeExtension) event(tabID int, method string, params any) {
	f.write(map[string]any{"type": "event", "event": map[string]any{
		"type": "cdpEvent",
		"data": map[string]any{"tabId": tabID, "method": method, "params": params},
	}})
}

func (f *fakeExtension) cdp(tabID int, method string, params json.RawMessage) (any, string) {
	switch method {
	case "Page.enable", "Runtime.enable", "Network.enable", "Accessibility.enable",
		"Emulation.setFocusEmulationEnabled", "Input.dispatchMouseEvent", "Input.dispatchKeyEvent",
		"Input.insertText", "Debugger.setBreakpointsActive", "Debugger.resume",
		"Page.handleJavaScriptDialog", "DOM.setFileInputFiles", "Target.setAutoAttach":
		return map[string]any{}, ""
	case "Page.navigate":
		var nav struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &nav)
		f.url = nav.URL
		// A real page commits first, then fires DOMContentLoaded and load.
		f.event(tabID, "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "F1", "url": nav.URL}})
		f.event(tabID, "Page.domContentEventFired", map[string]any{"timestamp": 0})
		f.event(tabID, "Page.loadEventFired", map[string]any{"timestamp": 0})
		return map[string]any{"frameId": "F1"}, ""
	case "Page.reload":
		return map[string]any{}, ""
	case "Page.getNavigationHistory":
		return map[string]any{"currentIndex": 0, "entries": []any{map[string]any{"id": 1}}}, ""
	case "Page.getLayoutMetrics":
		return map[string]any{"cssContentSize": map[string]any{"width": 800, "height": 600}}, ""
	case "Page.captureScreenshot":
		return map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("jpeg"))}, ""
	case "Page.printToPDF":
		return map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("%PDF"))}, ""
	case "Accessibility.getFullAXTree":
		return map[string]any{"nodes": []any{
			map[string]any{"nodeId": "1", "ignored": false, "role": map[string]any{"value": "WebArea"},
				"name": map[string]any{"value": "Example Page"}, "childIds": []string{"2"}},
			map[string]any{"nodeId": "2", "ignored": false, "role": map[string]any{"value": "button"},
				"name": map[string]any{"value": "Submit"}, "childIds": []string{}, "backendDOMNodeId": 42},
		}}, ""
	case "DOM.resolveNode":
		return map[string]any{"object": map[string]any{"objectId": "elem"}}, ""
	case "DOM.describeNode":
		return map[string]any{"node": map[string]any{"backendNodeId": 42}}, ""
	case "Network.getResponseBody":
		return map[string]any{"body": "response body"}, ""
	case "Runtime.evaluate":
		var eval struct {
			Expression    string `json:"expression"`
			ReturnByValue bool   `json:"returnByValue"`
		}
		_ = json.Unmarshal(params, &eval)
		expression := eval.Expression
		if strings.Contains(expression, "Array.from(document.querySelectorAll") || strings.Contains(expression, "(function(calls)") {
			return map[string]any{"result": map[string]any{"objectId": "array"}}, ""
		}
		if strings.Contains(expression, "document.activeElement") {
			return map[string]any{"result": map[string]any{"objectId": "active"}}, ""
		}
		switch {
		case strings.Contains(expression, "sample:"):
			return f.probeReply(expression), ""
		case strings.Contains(expression, "location.href"):
			return valueReply(f.url), ""
		case strings.Contains(expression, "document.title"):
			return valueReply("Example Page"), ""
		case strings.Contains(expression, "document.readyState"):
			return valueReply("complete"), ""
		case strings.Contains(expression, "visibilityState"):
			return valueReply(true), ""
		case strings.Contains(expression, "innerText"):
			return valueReply("Hello page"), ""
		case strings.Contains(expression, "innerWidth"):
			return valueReply(map[string]any{"width": 800, "height": 600}), ""
		}
		return valueReply(nil), ""
	case "Runtime.callFunctionOn":
		var call struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		_ = json.Unmarshal(params, &call)
		declaration := call.FunctionDeclaration
		switch {
		case strings.Contains(declaration, "elementFromPoint"):
			return valueReply(map[string]any{"ok": true, "x": 10, "y": 20, "mainFrame": true}), ""
		case strings.Contains(declaration, "this.length"):
			return valueReply(1), ""
		case strings.Contains(declaration, "this[0]"):
			return map[string]any{"result": map[string]any{"objectId": "elem"}}, ""
		case strings.Contains(declaration, "el.value = text"):
			return valueReply("typed"), ""
		case strings.Contains(declaration, "checked"):
			return valueReply(true), ""
		case strings.Contains(declaration, "getAttribute"):
			return valueReply("attr-value"), ""
		case strings.Contains(declaration, "innerText"):
			return valueReply("Hello"), ""
		case strings.Contains(declaration, "scrollTop"):
			return valueReply(map[string]any{"top": 0, "max": 0, "atBottom": true}), ""
		case strings.Contains(declaration, "getBoundingClientRect"):
			return valueReply(map[string]any{"x": 0, "y": 0, "width": 10, "height": 10}), ""
		}
		return valueReply(nil), ""
	}
	return nil, "unknown CDP method " + method
}

func valueReply(value any) map[string]any {
	return map[string]any{"result": map[string]any{"value": value}}
}

// --- fake thin process ---

type fakeClient struct {
	t       *testing.T
	ws      *websocket.Conn
	writeMu sync.Mutex
	nextID  int64
	written []string
}

func connectClient(t *testing.T, port int, token string) *fakeClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("ws://127.0.0.1:%d/client?token=%s", port, token)
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	client := &fakeClient{t: t, ws: ws}
	client.send(map[string]any{
		"type": "hello", "protocol": 1, "version": "test",
		"client":       map[string]any{"name": "claude-code", "version": "1"},
		"capabilities": []string{}, "pid": 1,
	})
	welcome := client.read()
	if welcome["type"] != "welcome" {
		t.Fatalf("client welcome: %v", welcome)
	}
	return client
}

func (c *fakeClient) send(frame any) {
	data, _ := json.Marshal(frame)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.Write(context.Background(), websocket.MessageText, data); err != nil {
		c.t.Errorf("client write: %v", err)
	}
}

func (c *fakeClient) read() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("client read: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		c.t.Fatalf("client frame: %v", err)
	}
	return frame
}

func (c *fakeClient) call(method string, params any) map[string]any {
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		frame := c.read()
		if frame["method"] != nil {
			c.answer(frame)
			continue
		}
		if number, ok := frame["id"].(float64); ok && int64(number) == id {
			return frame
		}
	}
}

func (c *fakeClient) answer(frame map[string]any) {
	if frame["method"] == "writeFile" {
		params := frame["params"].(map[string]any)
		path, _ := params["path"].(string)
		c.written = append(c.written, path)
		c.send(map[string]any{"id": frame["id"], "result": map[string]any{"path": path}})
		return
	}
	c.send(map[string]any{"id": frame["id"], "error": map[string]any{"message": "unexpected"}})
}

func (c *fakeClient) browser(t *testing.T, commands string) (string, bool) {
	t.Helper()
	return c.callTool(t, map[string]any{"commands": commands})
}

// callTool runs `tools/call` with any arguments, to test how a call the model
// gets wrong is answered.
func (c *fakeClient) callTool(t *testing.T, arguments map[string]any) (string, bool) {
	t.Helper()
	response := c.call("tools/call", map[string]any{"name": "browser", "arguments": arguments})
	if response["error"] != nil {
		t.Fatalf("tools/call: %v", response["error"])
	}
	result := response["result"].(map[string]any)
	content := result["content"].([]any)
	text := ""
	for _, part := range content {
		if entry := part.(map[string]any); entry["type"] == "text" {
			text += entry["text"].(string)
		}
	}
	_, isError := result["isError"].(bool)
	return text, isError
}

// --- tests ---

func TestNativeEndToEnd(t *testing.T) {
	instance, home, port := startNative(t)
	_ = instance
	extension := connectExtension(t, port, true)
	extension.sendState([]map[string]any{{
		"tabId": 1, "windowId": 1, "url": "about:blank", "title": "New Tab",
		"active": true, "pinned": false, "attached": false,
	}}, "paired")
	time.Sleep(100 * time.Millisecond)

	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		t.Fatalf("daemon.json: %v %v", info, err)
	}
	client := connectClient(t, port, info.Token)

	// The tool definition
	list := client.call("tools/list", map[string]any{})
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "browser" {
		t.Fatalf("tools: %v", tools)
	}

	// Help runs without a browser page
	text, isError := client.browser(t, "help")
	if isError || !strings.Contains(text, "goto <url>") {
		t.Fatalf("help: %q isError=%v", text, isError)
	}

	// tab list sees the extension's tab
	text, isError = client.browser(t, "tab list")
	if isError || !strings.Contains(text, "New Tab") {
		t.Fatalf("tab list: %q isError=%v", text, isError)
	}

	// goto: creates a tab, attaches, navigates
	text, isError = client.browser(t, "goto https://example.com")
	if isError || !strings.Contains(text, "✓ goto https://example.com") {
		t.Fatalf("goto: %q isError=%v", text, isError)
	}

	// goto --wait commit and --wait dom return once their event fires
	for _, mode := range []string{"commit", "dom"} {
		text, isError = client.browser(t, "goto https://example.com --wait "+mode)
		if isError || !strings.Contains(text, "✓ goto https://example.com --wait "+mode) {
			t.Fatalf("goto --wait %s: %q isError=%v", mode, text, isError)
		}
	}
	text, isError = client.browser(t, "goto https://example.com --wait nope")
	if !isError || !strings.Contains(text, "--wait is commit, dom or load") {
		t.Fatalf("goto --wait nope: %q isError=%v", text, isError)
	}

	// snapshot -i gives a ref
	text, isError = client.browser(t, "snapshot -i")
	if isError || !strings.Contains(text, `button "Submit" [ref=e1]`) {
		t.Fatalf("snapshot: %q isError=%v", text, isError)
	}
	if strings.Contains(text, "WebArea") {
		t.Fatalf("snapshot kept the unnamed root: %q", text)
	}

	// click resolves the ref, checks actionability and sends input
	text, isError = client.browser(t, "click e1")
	if isError || !strings.Contains(text, "✓ click e1") {
		t.Fatalf("click: %q isError=%v", text, isError)
	}

	// get text and eval
	text, _ = client.browser(t, "get text")
	if !strings.Contains(text, "Hello page") {
		t.Fatalf("get text: %q", text)
	}
	text, _ = client.browser(t, `eval "() => location.href"`)
	if !strings.Contains(text, "https://example.com") {
		t.Fatalf("eval: %q", text)
	}

	// screenshot --path writes through the client
	text, isError = client.browser(t, "screenshot --path shot.jpg")
	if isError {
		t.Fatalf("screenshot: %q", text)
	}
	if len(client.written) != 1 || client.written[0] != "shot.jpg" {
		t.Fatalf("writeFile requests: %v", client.written)
	}

	// A bad line runs none of the call
	text, isError = client.browser(t, "goto https://example.com\nnope")
	if !isError || !strings.Contains(text, "Unknown command nope") || strings.Contains(text, "✓ goto") {
		t.Fatalf("invalid line: %q isError=%v", text, isError)
	}
}

func TestNativeIdentitySurvivesRestart(t *testing.T) {
	home := t.TempDir()
	port1 := freeNativePort(t)
	first, err := daemon.Start(config.Config{Home: home, Port: port1, Version: "test", IdleExit: time.Minute, Native: true})
	if err != nil {
		t.Fatal(err)
	}
	extension := connectExtension(t, port1, true)
	key := extension.publicKey
	first.Stop("restart")
	<-first.Stopped()

	port2 := freeNativePort(t)
	second, err := daemon.Start(config.Config{Home: home, Port: port2, Version: "test", IdleExit: time.Minute, Native: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Stop("test"); <-second.Stopped() })
	again := connectExtension(t, port2, true)
	if again.publicKey != key {
		t.Fatalf("the daemon made a new pairing key across a restart: %q -> %q", key, again.publicKey)
	}
}

func TestNativePairingGate(t *testing.T) {
	_, home, port := startNative(t)
	extension := connectExtension(t, port, false)
	time.Sleep(100 * time.Millisecond)
	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		t.Fatalf("daemon.json: %v %v", info, err)
	}
	client := connectClient(t, port, info.Token)

	text, isError := client.browser(t, "tab list")
	if !isError || !strings.Contains(text, "Click Allow") {
		t.Fatalf("unpaired call: %q isError=%v", text, isError)
	}

	extension.write(map[string]any{"type": "pairingNonce", "nonce": base64.StdEncoding.EncodeToString([]byte("extension-nonce-0123456789abcdef"))})
	time.Sleep(100 * time.Millisecond)
	text, isError = client.browser(t, "tab list")
	if !isError || !strings.Contains(text, "The code is ") {
		t.Fatalf("pairing code after nonce: %q isError=%v", text, isError)
	}

	extension.sendState([]map[string]any{{
		"tabId": 1, "windowId": 1, "url": "about:blank", "title": "New Tab",
		"active": true, "pinned": false, "attached": false,
	}}, "paired")
	time.Sleep(100 * time.Millisecond)
	text, isError = client.browser(t, "tab list")
	if isError || !strings.Contains(text, "New Tab") {
		t.Fatalf("paired call: %q isError=%v", text, isError)
	}
}

func rawClient(t *testing.T, port int, token string) *fakeClient {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("ws://127.0.0.1:%d/client?token=%s", port, token)
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	c := &fakeClient{t: t, ws: ws}
	c.send(map[string]any{"type": "hello", "protocol": 1, "version": "test", "client": map[string]any{"name": "claude-code"}, "capabilities": []string{}, "pid": 1})
	return c
}

// --- guidance, waits and tab cleanup ---

// connectedClient is a daemon, a paired extension with one tab of the user's,
// and a client, which most of these tests start from.
func connectedClient(t *testing.T) (*fakeClient, *fakeExtension) {
	t.Helper()
	_, home, port := startNative(t)
	extension := connectExtension(t, port, true)
	extension.sendState([]map[string]any{{
		"tabId": 1, "windowId": 1, "url": "about:blank", "title": "New Tab",
		"active": true, "pinned": false, "attached": false,
	}}, "paired")
	time.Sleep(100 * time.Millisecond)
	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		t.Fatalf("daemon.json: %v %v", info, err)
	}
	return connectClient(t, port, info.Token), extension
}

func TestNativeCommandGuidance(t *testing.T) {
	client, _ := connectedClient(t)

	// The argument is named when the call sends it under another key
	text, isError := client.callTool(t, map[string]any{"command": "help"})
	if !isError || !strings.Contains(text, "`command`") || !strings.Contains(text, "Did you mean `commands`?") {
		t.Fatalf("a wrong argument key: %q isError=%v", text, isError)
	}

	// An array of lines works as one string does
	text, isError = client.callTool(t, map[string]any{"commands": []any{"tab list"}})
	if isError || !strings.Contains(text, "New Tab") {
		t.Fatalf("an array of lines: %q isError=%v", text, isError)
	}

	// A bare word of a multi-word command points at both forms
	text, isError = client.browser(t, "list")
	if !isError || !strings.Contains(text, "Unknown command list") ||
		!strings.Contains(text, "`tab list`") || !strings.Contains(text, "`browser list`") {
		t.Fatalf("a bare word: %q isError=%v", text, isError)
	}

	// A typo is matched to the command it meant
	text, isError = client.browser(t, "clik e1")
	if !isError || !strings.Contains(text, "Did you mean `click`?") {
		t.Fatalf("a typo: %q isError=%v", text, isError)
	}

	// Nothing close: the way to see every command
	text, isError = client.browser(t, "frobnicate")
	if !isError || !strings.Contains(text, "Unknown command frobnicate") || !strings.Contains(text, "Run `help`") {
		t.Fatalf("an unknown command: %q isError=%v", text, isError)
	}
}

func TestNativeWaitTimeoutNamesThePageState(t *testing.T) {
	client, extension := connectedClient(t)
	extension.setPage("Where should we begin?", "", "complete")
	if text, isError := client.browser(t, "goto https://chatgpt.com"); isError {
		t.Fatalf("goto: %q", text)
	}

	// The text is nowhere: the message says what the page does show
	text, isError := client.browser(t, `wait --text "Message ChatGPT" --timeout 300`)
	if !isError || !strings.Contains(text, `didn't appear within`) {
		t.Fatalf("a wait for text: %q isError=%v", text, isError)
	}
	if !strings.Contains(text, `the page shows "Where should we begin?"`) {
		t.Fatalf("the timeout does not say what the page shows: %q", text)
	}
	if strings.Contains(text, "navigated") {
		t.Fatalf("the timeout reports a navigation that did not happen: %q", text)
	}

	// A placeholder is not visible text: the message says where the text is
	extension.setPage("Where should we begin?", `<textarea placeholder="Message ChatGPT"></textarea>`, "complete")
	text, isError = client.browser(t, `wait --text "Message ChatGPT" --timeout 300`)
	if !isError || !strings.Contains(text, "in the page's HTML but not in its visible text") {
		t.Fatalf("a placeholder: %q isError=%v", text, isError)
	}

	// A page that has not finished loading says so
	extension.setPage("", "", "loading")
	text, isError = client.browser(t, `wait --text "Message ChatGPT" --timeout 300`)
	if !isError || !strings.Contains(text, "still loading (readyState loading)") {
		t.Fatalf("a loading page: %q isError=%v", text, isError)
	}

	// The text on the page ends the wait with no error
	extension.setPage("Message ChatGPT", "", "complete")
	if text, isError := client.browser(t, `wait --text "Message ChatGPT" --timeout 300`); isError || !strings.Contains(text, "✓ wait") {
		t.Fatalf("a text that appears: %q isError=%v", text, isError)
	}
}

func TestNativeTabsSurviveAFailedCallAndStayTheUser(t *testing.T) {
	client, _ := connectedClient(t)

	// The second step fails after goto opened a tab: the call stops, the tab stays
	text, isError := client.browser(t, "goto https://example.com\nwait --text \"never there\" --timeout 200")
	if !isError || !strings.Contains(text, "✗ step 2") {
		t.Fatalf("a failed call: %q isError=%v", text, isError)
	}
	if strings.Contains(text, "✓ wait") {
		t.Fatalf("a failed call ran its later step: %q", text)
	}

	// Cleanup: the tab the call opened is still the agent's to close
	if text, isError := client.browser(t, "tab close 2"); isError || !strings.Contains(text, "✓ tab close 2") {
		t.Fatalf("closing the opened tab after a failure: %q isError=%v", text, isError)
	}

	// The user's own tab is never closed
	if text, isError := client.browser(t, "tab select 1"); isError {
		t.Fatalf("tab select 1: %q", text)
	}
	text, isError = client.browser(t, "tab close 1")
	if !isError || !strings.Contains(text, "isn't one you opened") {
		t.Fatalf("closing the user's tab: %q isError=%v", text, isError)
	}
}

func TestNativeNoBrowserConnected(t *testing.T) {
	_, home, port := startNative(t)
	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		t.Fatalf("daemon.json: %v %v", info, err)
	}
	client := connectClient(t, port, info.Token)
	text, isError := client.browser(t, "tab list")
	if !isError || !strings.Contains(text, "No browser is connected") {
		t.Fatalf("no extension: %q isError=%v", text, isError)
	}
}
