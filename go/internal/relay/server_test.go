package relay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/protocol"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/relay"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/worker"
)

type fakeWorker struct {
	server  *httptest.Server
	origins chan string
}

func newFakeWorker(t *testing.T) *fakeWorker {
	t.Helper()
	fake := &fakeWorker{origins: make(chan string, 4)}
	mux := http.NewServeMux()
	echo := func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}
	mux.HandleFunc(protocol.PathClient, echo)
	mux.HandleFunc(protocol.PathExtension, func(w http.ResponseWriter, r *http.Request) {
		fake.origins <- r.Header.Get("Origin")
		echo(w, r)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeWorker) endpoint(t *testing.T) *worker.Endpoint {
	t.Helper()
	parsed, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return &worker.Endpoint{Port: port, Token: "worker-secret"}
}

type staticEngine struct{ endpoint *worker.Endpoint }

func (e *staticEngine) Ensure(context.Context) (*worker.Endpoint, error) {
	return e.endpoint, nil
}
func (e *staticEngine) Stop(context.Context) {}

type harness struct {
	server   *relay.Server
	addr     string
	shutdown chan struct{}
	clients  atomic.Int32
}

func startRelay(t *testing.T, engine relay.Engine) *harness {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{shutdown: make(chan struct{}, 1)}
	h.server = relay.New(listener, relay.Options{
		Token:   "secret",
		Version: "test-version",
		Engine:  engine,
		Home:    t.TempDir(),
		OnShutdown: func() {
			select {
			case h.shutdown <- struct{}{}:
			default:
			}
		},
		OnClientOpen:  func() { h.clients.Add(1) },
		OnClientClose: func() { h.clients.Add(-1) },
	})
	go func() { _ = h.server.Serve() }()
	t.Cleanup(func() { _ = h.server.Close() })
	h.addr = listener.Addr().String()
	return h
}

func dialWS(t *testing.T, target string, header http.Header) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPHeader: header})
	return conn, err
}

func TestStatus(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	response, err := http.Get("http://" + h.addr + protocol.PathStatus)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var status protocol.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Product != protocol.Product || status.Version != "test-version" || status.PID != os.Getpid() {
		t.Fatalf("status = %#v", status)
	}
}

func TestHostHeaderIsRejected(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	request, err := http.NewRequest(http.MethodGet, "http://"+h.addr+protocol.PathStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "evil.example"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestStatusCORS(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	origin := "chrome-extension://" + protocol.SelfBuiltExtensionID
	request, err := http.NewRequest(http.MethodOptions, "http://"+h.addr+protocol.PathStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
	if got := response.Header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestClientToken(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	bad := "ws://" + h.addr + protocol.PathClient + "?token=wrong"
	if _, err := dialWS(t, bad, nil); err == nil {
		t.Fatal("dial with a bad token succeeded")
	}

	conn, err := dialWS(t, "ws://"+h.addr+protocol.PathClient+"?token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "ping" {
		t.Fatalf("read = %q, %v", data, err)
	}
	waitFor(t, func() bool { return h.clients.Load() == 1 })
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return h.clients.Load() == 0 })
}

func TestExtensionOrigin(t *testing.T) {
	fake := newFakeWorker(t)
	h := startRelay(t, &staticEngine{endpoint: fake.endpoint(t)})
	origin := "chrome-extension://" + protocol.ChromeWebStoreExtensionID
	conn, err := dialWS(t, "ws://"+h.addr+protocol.PathExtension, http.Header{"Origin": {origin}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read = %q, %v", data, err)
	}
	select {
	case got := <-fake.origins:
		if got != origin {
			t.Fatalf("worker saw Origin %q, want %q", got, origin)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not see the extension")
	}

	bad := "ws://" + h.addr + protocol.PathExtension
	if _, err := dialWS(t, bad, http.Header{"Origin": {"https://evil.example"}}); err == nil {
		t.Fatal("dial with a bad Origin succeeded")
	}
}

func TestClientHelloExtensionIDsAreAllowed(t *testing.T) {
	t.Setenv("BROWSER_MCP_EXTENSION_IDS", "")
	fake := newFakeWorker(t)
	h := startRelay(t, &staticEngine{endpoint: fake.endpoint(t)})
	const devID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	client, err := dialWS(t, "ws://"+h.addr+protocol.PathClient+"?token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(websocket.StatusNormalClosure, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hello, err := json.Marshal(map[string]any{
		"type":         "hello",
		"protocol":     1,
		"extensionIds": []string{devID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	// The fake worker echoes the hello once the front door forwards it.
	if _, _, err := client.Read(ctx); err != nil {
		t.Fatal(err)
	}

	extension, err := dialWS(t, "ws://"+h.addr+protocol.PathExtension,
		http.Header{"Origin": {"chrome-extension://" + devID}})
	if err != nil {
		t.Fatalf("extension with a hello-provided ID was rejected: %v", err)
	}
	defer extension.Close(websocket.StatusNormalClosure, "")
	if err := extension.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	_, data, err := extension.Read(ctx)
	if err != nil || string(data) != "hi" {
		t.Fatalf("extension read = %q, %v", data, err)
	}
}

func TestShutdown(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	response, err := http.Post("http://"+h.addr+protocol.PathShutdown+"?token=secret", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	select {
	case <-h.shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("OnShutdown was not called")
	}
}

func TestCDPIsNotExposed(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	response, err := http.Get("http://" + h.addr + protocol.PathCDP)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestConcurrentClients(t *testing.T) {
	h := startRelay(t, &staticEngine{endpoint: newFakeWorker(t).endpoint(t)})
	const clients = 20
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			conn, err := dialWS(t, "ws://"+h.addr+protocol.PathClient+"?token=secret", nil)
			if err != nil {
				t.Errorf("client %d: %v", index, err)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			message := fmt.Sprintf("client-%d", index)
			if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
				t.Errorf("client %d write: %v", index, err)
				return
			}
			_, data, err := conn.Read(ctx)
			if err != nil || string(data) != message {
				t.Errorf("client %d read = %q, %v", index, data, err)
			}
		}(i)
	}
	wg.Wait()
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
