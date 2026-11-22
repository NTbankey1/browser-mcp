package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/proxy"
)

func newWSServer(t *testing.T) (*httptest.Server, <-chan *websocket.Conn) {
	t.Helper()
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		connections <- conn
	}))
	t.Cleanup(server.Close)
	return server, connections
}

func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func TestPipeCopiesBothWaysAndPropagatesClose(t *testing.T) {
	leftServer, leftConnections := newWSServer(t)
	rightServer, rightConnections := newWSServer(t)
	leftClient := dial(t, leftServer)
	rightClient := dial(t, rightServer)
	left := <-leftConnections
	right := <-rightConnections

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- proxy.Pipe(ctx, leftClient, rightClient) }()

	if err := left.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, data, err := right.Read(ctx)
	if err != nil || string(data) != "hello" {
		t.Fatalf("right read = %q, %v", data, err)
	}
	if err := right.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	typ, data, err := left.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || len(data) != 3 {
		t.Fatalf("left read = %v %v, %v", typ, data, err)
	}

	left.Close(websocket.StatusNormalClosure, "done")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not finish after a close")
	}
}
