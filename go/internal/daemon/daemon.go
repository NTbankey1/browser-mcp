// Package daemon owns the front door's lifecycle: port election, daemon.json,
// idle exit, signals and the engine worker.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/brand"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/browser"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/client"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	cmdengine "github.com/NTbankey1/browser-mcp/go-daemon/internal/engine"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/extension"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/pairing"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/protocol"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/relay"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/worker"
)

// ErrAlreadyRunning means another daemon holds the port (the thin process
// should use it). The front door exits successfully in that case.
var ErrAlreadyRunning = errors.New("another " + brand.Name + " daemon is already listening")

// Daemon is a running front door.
type Daemon struct {
	cfg        config.Config
	token      string
	port       int
	server     *relay.Server
	engine     *worker.Manager
	nativeStop func(context.Context)
	logger     *slog.Logger
	logFile    *os.File
	stopped    chan string
	stopOnce   sync.Once
	stopping   atomic.Bool
	idleMu     sync.Mutex
	idle       *time.Timer
	clients    int
}

// Start binds the port, writes daemon.json and serves until Stop.
func Start(cfg config.Config) (*Daemon, error) {
	if err := config.EnsureHome(cfg.Home); err != nil {
		return nil, err
	}
	listener, port, err := listen(cfg.Port)
	if err != nil {
		return nil, err
	}
	token, err := config.NewToken()
	if err != nil {
		listener.Close()
		return nil, err
	}
	logFile, err := os.OpenFile(config.LogPath(cfg.Home, cfg.Port), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		listener.Close()
		return nil, err
	}
	var output io.Writer = logFile
	if cfg.Stderr {
		output = io.MultiWriter(logFile, os.Stderr)
	}
	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))

	advertised := cfg.AdvertisedPort
	if advertised == 0 {
		advertised = port
	}
	d := &Daemon{
		cfg:     cfg,
		token:   token,
		port:    port,
		logger:  logger,
		logFile: logFile,
		stopped: make(chan string, 1),
	}
	options := relay.Options{
		Token:         token,
		Version:       cfg.Version,
		Logger:        logger,
		Home:          cfg.Home,
		OnClientOpen:  d.clientOpen,
		OnClientClose: d.clientClose,
		OnShutdown:    func() { go d.Stop("shutdown") },
	}
	var engineManager *worker.Manager
	var nativeStop func(context.Context)
	if cfg.Native {
		identity, err := pairing.LoadOrCreate(cfg.Home)
		if err != nil {
			listener.Close()
			logFile.Close()
			return nil, err
		}
		registry := browser.New(browser.Options{Log: logger})
		commandEngine := cmdengine.New(cmdengine.Options{Browsers: registry, Log: logger})
		nativeStop = func(ctx context.Context) {
			commandEngine.Close()
			registry.ReleaseAll(ctx)
		}
		options.AcceptExtension = func(ctx context.Context, conn *websocket.Conn) error {
			_, err := registry.AcceptExtension(ctx, conn, extension.Options{
				Identity: identity, Version: cfg.Version, Port: advertised, Log: logger,
			})
			return err
		}
		options.AcceptClient = func(ctx context.Context, conn *websocket.Conn, hello []byte) error {
			return client.Accept(ctx, conn, hello, commandEngine, client.Options{Version: cfg.Version, Log: logger})
		}
		logger.Info("native engine enabled")
	} else {
		engineManager = worker.New(cfg.Home, advertised, cfg.EngineCommand, logger)
		options.Engine = engineManager
	}
	d.engine = engineManager
	d.nativeStop = nativeStop
	d.server = relay.New(listener, options)

	info := config.Info{
		Port:    port,
		PID:     os.Getpid(),
		Version: cfg.Version,
		Token:   token,
	}
	if err := config.WriteInfo(config.InfoPath(cfg.Home, cfg.Port), info); err != nil {
		listener.Close()
		logFile.Close()
		return nil, err
	}

	logger.Info("daemon listening", "port", port, "version", cfg.Version)
	go func() {
		if err := d.server.Serve(); err != nil {
			logger.Error("relay stopped", "error", err)
			d.Stop("serve-error")
		}
	}()
	d.armIdle()
	return d, nil
}

// Stop shuts down the server, the worker and daemon.json once.
func (d *Daemon) Stop(reason string) {
	d.stopOnce.Do(func() {
		d.stopping.Store(true)
		d.idleMu.Lock()
		if d.idle != nil {
			d.idle.Stop()
			d.idle = nil
		}
		d.idleMu.Unlock()

		config.RemoveInfo(config.InfoPath(d.cfg.Home, d.cfg.Port), os.Getpid())
		_ = d.server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if d.nativeStop != nil {
			d.nativeStop(ctx)
		} else if d.engine != nil {
			d.engine.Stop(ctx)
		}
		cancel()
		d.logger.Info("daemon stopped", "reason", reason)
		_ = d.logFile.Close()
		d.stopped <- reason
		close(d.stopped)
	})
}

// Port is the port the front door listens on.
func (d *Daemon) Port() int { return d.port }

// Token is the token /client and /shutdown require.
func (d *Daemon) Token() string { return d.token }

// Stopped receives the stop reason when the daemon exits.
func (d *Daemon) Stopped() <-chan string { return d.stopped }

func (d *Daemon) clientOpen() {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	d.clients++
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
}

func (d *Daemon) clientClose() {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	d.clients--
	if d.clients <= 0 && !d.stopping.Load() {
		d.armIdleLocked()
	}
}

func (d *Daemon) armIdle() {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	d.armIdleLocked()
}

func (d *Daemon) armIdleLocked() {
	if d.cfg.IdleExit <= 0 || d.stopping.Load() {
		return
	}
	if d.idle != nil {
		d.idle.Stop()
	}
	d.idle = time.AfterFunc(d.cfg.IdleExit, func() {
		d.idleMu.Lock()
		empty := d.clients <= 0
		d.idleMu.Unlock()
		if empty {
			d.Stop("idle")
		}
	})
}

func listen(port int) (net.Listener, int, error) {
	if port > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", ErrAlreadyRunning, err)
		}
		return listener, port, nil
	}
	for _, candidate := range protocol.Ports {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", candidate))
		if err == nil {
			return listener, candidate, nil
		}
	}
	return nil, 0, ErrAlreadyRunning
}
