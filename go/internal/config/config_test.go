package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewTokenIsURLSafe(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("token length = %d, want 43", len(token))
	}
	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token is not raw base64url: %q", token)
	}
}

func TestWriteReadRemoveInfo(t *testing.T) {
	home := t.TempDir()
	path := InfoPath(home, 0)
	info := Info{Port: 9411, PID: os.Getpid(), Version: "2.5.0", Token: "secret"}
	if err := WriteInfo(path, info); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", stat.Mode().Perm())
	}
	read, err := ReadInfo(path)
	if err != nil || read == nil {
		t.Fatalf("ReadInfo = %#v, %v", read, err)
	}
	if *read != info {
		t.Fatalf("ReadInfo = %#v, want %#v", read, info)
	}
	RemoveInfo(path, os.Getpid()+1)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("RemoveInfo removed a file with another pid: %v", err)
	}
	RemoveInfo(path, os.Getpid())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("RemoveInfo left the file: %v", err)
	}
}

func TestPackageVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := PackageVersion(filepath.Join(dir, "bin", "browsermcpd"), "0.0.0")
	if got != "9.9.9" {
		t.Fatalf("PackageVersion = %q, want 9.9.9", got)
	}
	other := t.TempDir()
	if got := PackageVersion(filepath.Join(other, "bin", "browsermcpd"), "1.2.3"); got != "1.2.3" {
		t.Fatalf("PackageVersion fallback = %q, want 1.2.3", got)
	}
}

func TestPortPaths(t *testing.T) {
	home := t.TempDir()
	if got := InfoPath(home, 0); filepath.Base(got) != "daemon.json" {
		t.Fatalf("InfoPath = %s", got)
	}
	if got := InfoPath(home, 9300); filepath.Base(got) != "daemon-9300.json" {
		t.Fatalf("InfoPath = %s", got)
	}
	if got := LogPath(home, 9300); filepath.Base(got) != "daemon-9300.log" {
		t.Fatalf("LogPath = %s", got)
	}
}
