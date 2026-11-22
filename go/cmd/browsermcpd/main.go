// browsermcpd is the Go front door for Browser MCP. It owns the public
// loopback port, daemon.json, lifecycle and security checks, and proxies the
// client and extension WebSockets to the TypeScript engine worker.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/config"
	"github.com/NTbankey1/browser-mcp/go-daemon/internal/daemon"
)

// version is injected at build time with -X main.version=<package version>.
var version = "0.0.0"

func main() {
	flags := flag.NewFlagSet("browsermcpd", flag.ExitOnError)
	port := flags.Int("port", 0, "listen on this port only")
	advertised := flags.Int("advertised-port", 0, "port the extension dialed (default: the listening port)")
	engine := flags.String("engine", "", "engine command (default: node <binary dir>/daemon.js)")
	stderr := flags.Bool("stderr", false, "also log to stderr")
	idle := flags.Duration("idle-exit", 10*time.Minute, "exit after this long with no client (0 disables)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	_ = flags.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println(version)
		return
	}
	home, err := config.Home()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg := config.Config{
		Home:           home,
		Port:           *port,
		Version:        version,
		AdvertisedPort: *advertised,
		IdleExit:       *idle,
		Stderr:         *stderr,
	}
	if *engine != "" {
		cfg.EngineCommand = strings.Fields(*engine)
	}
	instance, err := daemon.Start(cfg)
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		fmt.Fprintln(os.Stderr, "Another Browser MCP daemon is already listening")
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case signal := <-signals:
		instance.Stop(signal.String())
	case reason := <-instance.Stopped():
		_ = reason
	}
}
