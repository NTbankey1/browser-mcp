package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/daemon"
)

// TestFrontDoorWithNodeWorker starts the real Go front door, which starts the
// real TypeScript engine worker, and checks the MCP client RPC path end to end:
// hello/welcome, tools/list and tools/call help.
func TestFrontDoorWithNodeWorker(t *testing.T) {
	repoRoot := repoRoot(t)
	daemonJs := filepath.Join(repoRoot, "packages", "mcp", "dist", "daemon.js")
	requireBuild(t, daemonJs)

	home := t.TempDir()
	instance, err := daemon.Start(config.Config{
		Home:          home,
		Port:          freePort(t),
		Version:       "2.5.0",
		EngineCommand: []string{"node", daemonJs},
		IdleExit:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Stop("test")

	statusURL := fmt.Sprintf("http://127.0.0.1:%d%s", instance.Port(), "/browsermcp/status")
	response, err := http.Get(statusURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target := fmt.Sprintf("ws://127.0.0.1:%d/client?token=%s", instance.Port(), instance.Token())
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(16 << 20)

	writeJSON(t, ctx, conn, map[string]any{
		"type":         "hello",
		"protocol":     1,
		"version":      "0.0.0-test",
		"client":       map[string]any{"name": "go-integration"},
		"capabilities": []any{},
		"pid":          1234,
	})
	welcome := readJSON(t, ctx, conn)
	if welcome["type"] != "welcome" {
		t.Fatalf("welcome = %#v", welcome)
	}

	writeJSON(t, ctx, conn, map[string]any{"id": 1, "method": "tools/list", "params": map[string]any{}})
	responseJSON := readResponse(t, ctx, conn, 1)
	assertBrowserTool(t, responseJSON)

	writeJSON(t, ctx, conn, map[string]any{
		"id":     2,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "browser",
			"arguments": map[string]any{"commands": "help"},
		},
	})
	responseJSON = readResponse(t, ctx, conn, 2)
	assertContent(t, responseJSON)
}

// TestThinProcessUsesGoFrontDoor runs the built thin process with
// BROWSER_MCP_DAEMON=go and speaks MCP over stdio, proving the full path:
// AI-client stdio -> thin -> Go front door -> Node engine worker.
func TestThinProcessUsesGoFrontDoor(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the thin process, the Go front door and the Node worker")
	}
	repoRoot := repoRoot(t)
	indexJs := filepath.Join(repoRoot, "packages", "mcp", "dist", "index.js")
	binary := filepath.Join(repoRoot, "packages", "mcp", "dist", "browsermcpd")
	requireBuild(t, indexJs)
	requireBuild(t, binary)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	home := t.TempDir()
	port := freePort(t)
	command := exec.Command(node, indexJs, "--port", strconv.Itoa(port))
	command.Env = append(os.Environ(),
		"BROWSER_MCP_HOME="+home,
		"BROWSER_MCP_DAEMON=go",
		"BROWSER_MCP_GO_DAEMON="+binary,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		shutdownFrontDoor(home, port)
	}()

	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var stderrMu sync.Mutex
	var stderrText string
	stderrSnapshot := func() string {
		stderrMu.Lock()
		defer stderrMu.Unlock()
		return stderrText
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrMu.Lock()
			stderrText += scanner.Text() + "\n"
			stderrMu.Unlock()
		}
	}()

	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	readID := func(id int) map[string]any {
		timeout := time.After(60 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("thin process closed stdout\n%s", stderrSnapshot())
				}
				var message map[string]any
				if err := json.Unmarshal([]byte(line), &message); err != nil {
					continue
				}
				if number, ok := message["id"].(float64); ok && int(number) == id {
					return message
				}
			case <-timeout:
				t.Fatalf("timed out waiting for id %d\n%s", id, stderrSnapshot())
			}
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "go-integration", "version": "1"},
		},
	})
	readID(1)
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	assertBrowserTool(t, readID(2))

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "browser",
			"arguments": map[string]any{"commands": "help"},
		},
	})
	assertContent(t, readID(3))
}

func assertBrowserTool(t *testing.T, response map[string]any) {
	t.Helper()
	result, _ := response["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("tools/list = %#v", response)
	}
	for _, raw := range tools {
		if tool, ok := raw.(map[string]any); ok && tool["name"] == "browser" {
			return
		}
	}
	t.Fatalf("browser tool missing: %#v", tools)
}

func assertContent(t *testing.T, response map[string]any) {
	t.Helper()
	result, _ := response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call help = %#v", response)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func requireBuild(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built: run pnpm build and pnpm --filter @browsermcp/mcp go:build first", path)
	}
}

func shutdownFrontDoor(home string, port int) {
	info, err := config.ReadInfo(config.InfoPath(home, port))
	if err != nil || info == nil {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/shutdown?token=%s", info.Port, info.Token), nil)
	if err != nil {
		return
	}
	response, err := client.Do(request)
	if err == nil {
		response.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(config.InfoPath(home, port)); os.IsNotExist(err) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func writeJSON(t *testing.T, ctx context.Context, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("frame %q: %v", data, err)
	}
	return value
}

func readResponse(t *testing.T, ctx context.Context, conn *websocket.Conn, id int) map[string]any {
	t.Helper()
	for {
		message := readJSON(t, ctx, conn)
		if number, ok := message["id"].(float64); ok && int(number) == id {
			return message
		}
		if message["type"] == "welcome" {
			continue
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
