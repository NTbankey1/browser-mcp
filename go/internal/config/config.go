// Package config owns the home directory, daemon.json, ports and tokens.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Home is BROWSER_MCP_HOME, or ~/.browsermcp.
func Home() (string, error) {
	if home := os.Getenv("BROWSER_MCP_HOME"); home != "" {
		return home, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".browsermcp"), nil
}

// EnsureHome creates the directory with mode 0700.
func EnsureHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	// MkdirAll does not change the mode of an existing directory.
	return os.Chmod(home, 0o700)
}

// Info is daemon.json, read by the thin processes.
type Info struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	Token   string `json:"token"`
}

// InfoPath is daemon.json, or daemon-<port>.json for --port.
func InfoPath(home string, port int) string {
	if port > 0 {
		return filepath.Join(home, fmt.Sprintf("daemon-%d.json", port))
	}
	return filepath.Join(home, "daemon.json")
}

// LogPath is daemon.log, or daemon-<port>.log for --port.
func LogPath(home string, port int) string {
	if port > 0 {
		return filepath.Join(home, fmt.Sprintf("daemon-%d.log", port))
	}
	return filepath.Join(home, "daemon.log")
}

// DevExtensionIDsPath is the file dev tooling appends extension IDs to.
func DevExtensionIDsPath(home string) string {
	return filepath.Join(home, "dev-extension-ids")
}

// NewToken is 32 random bytes, base64url without padding, like the Node daemon.
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// WriteInfo writes daemon.json atomically with mode 0600.
func WriteInfo(path string, info Info) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	temp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return err
	}
	return nil
}

// ReadInfo reads daemon.json, returning nil when it is absent or unreadable.
func ReadInfo(path string) (*Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, nil
	}
	if info.Port <= 0 || info.Token == "" {
		return nil, nil
	}
	return &info, nil
}

// RemoveInfo removes daemon.json only when it still names this PID.
func RemoveInfo(path string, pid int) {
	info, err := ReadInfo(path)
	if err == nil && info != nil && info.PID == pid {
		_ = os.Remove(path)
	}
}

// Config is the front door's runtime configuration.
type Config struct {
	Home           string
	Port           int
	Version        string
	EngineCommand  []string
	AdvertisedPort int
	IdleExit       time.Duration
	Stderr         bool
	// Native runs the command engine in this process instead of proxying to
	// the TypeScript engine worker.
	Native bool
}
