// Package worker starts and stops the TypeScript engine worker. The worker is
// the existing @browsermcp/mcp daemon, run on an internal loopback port with
// the public port advertised for pairing. The front door proxies /client and
// /extension to it, so every browser command keeps working unchanged.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/protocol"
)

// Endpoint is a running worker's internal port and token.
type Endpoint struct {
	Port  int
	Token string
}

func (e *Endpoint) ClientURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d%s?token=%s", e.Port, protocol.PathClient, e.Token)
}

func (e *Endpoint) ExtensionURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d%s", e.Port, protocol.PathExtension)
}

func (e *Endpoint) StatusURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", e.Port, protocol.PathStatus)
}

func (e *Endpoint) ShutdownURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s?token=%s", e.Port, protocol.PathShutdown, e.Token)
}

// Manager lazily starts one worker for the front door and restarts it if it
// exits. It is safe for concurrent use.
type Manager struct {
	mu         sync.Mutex
	home       string
	publicPort int
	command    []string
	log        *slog.Logger
	endpoint   *Endpoint
	cmd        *exec.Cmd
	done       chan struct{}
}

// New returns a manager. An empty command means `node <binary dir>/daemon.js`.
func New(home string, publicPort int, command []string, logger *slog.Logger) *Manager {
	if len(command) == 0 {
		exe, err := os.Executable()
		if err != nil {
			exe = "."
		}
		command = []string{"node", filepath.Join(filepath.Dir(exe), "daemon.js")}
	}
	return &Manager{
		home:       home,
		publicPort: publicPort,
		command:    command,
		log:        logger,
	}
}

// Ensure returns a ready worker, starting one if necessary.
func (m *Manager) Ensure(ctx context.Context) (*Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.endpoint != nil && m.alive() {
		return m.endpoint, nil
	}
	m.reset()

	workerHome := filepath.Join(m.home, fmt.Sprintf("engine-%d", m.publicPort))
	if err := config.EnsureHome(workerHome); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}

	args := append([]string{}, m.command...)
	args = append(args,
		"--port", strconv.Itoa(port),
		"--advertised-port", strconv.Itoa(m.publicPort),
	)
	args = append(args, keepWorkerAliveArgs()...)
	cmd := exec.Command(args[0], args[1:]...)
	setParentDeathSignal(cmd)
	cmd.Env = append(os.Environ(), "BROWSER_MCP_HOME="+workerHome)
	// The worker writes its own log; stdout/stderr are discarded.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start engine worker: %w", err)
	}
	done := make(chan struct{})
	m.cmd = cmd
	m.done = done
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	infoPath := config.InfoPath(workerHome, port)
	deadline := time.Now().Add(20 * time.Second)
	for {
		info, _ := config.ReadInfo(infoPath)
		if info != nil && info.PID == cmd.Process.Pid && workerAnswers(ctx, port) {
			m.endpoint = &Endpoint{Port: port, Token: info.Token}
			if m.log != nil {
				m.log.Info("engine worker ready", "port", port, "pid", info.PID)
			}
			return m.endpoint, nil
		}
		select {
		case <-ctx.Done():
			m.stopLocked()
			return nil, ctx.Err()
		case <-done:
			m.reset()
			return nil, errors.New("engine worker exited during startup")
		case <-time.After(25 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			m.stopLocked()
			return nil, errors.New("engine worker did not become ready")
		}
	}
}

// Stop asks the worker to shut down, then kills it if it does not.
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	_ = ctx
}

func (m *Manager) stopLocked() {
	if m.cmd == nil {
		return
	}
	endpoint := m.endpoint
	if endpoint != nil {
		client := &http.Client{Timeout: 5 * time.Second}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.ShutdownURL(), nil)
		if err == nil {
			response, err := client.Do(request)
			if err == nil {
				response.Body.Close()
			}
		}
	}
	if m.done != nil {
		select {
		case <-m.done:
		case <-time.After(5 * time.Second):
			_ = m.cmd.Process.Kill()
			<-m.done
		}
	}
	m.reset()
}

func (m *Manager) alive() bool {
	if m.cmd == nil || m.done == nil {
		return false
	}
	select {
	case <-m.done:
		return false
	default:
		return true
	}
}

func (m *Manager) reset() {
	m.endpoint = nil
	m.cmd = nil
	m.done = nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func workerAnswers(ctx context.Context, port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, protocol.PathStatus), nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
