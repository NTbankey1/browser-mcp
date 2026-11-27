package engine

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// checkAllowed refuses a navigation outside BROWSER_MCP_ALLOW_DOMAINS, when that
// variable lists domains (comma separated; a leading dot or a bare domain allows
// its subdomains too). Unset means every site is allowed, as before.
func checkAllowed(rawURL string) error {
	list := strings.TrimSpace(os.Getenv("BROWSER_MCP_ALLOW_DOMAINS"))
	if list == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return nil // relative URLs and about: pages carry no host to check
	}
	host := strings.ToLower(parsed.Hostname())
	for _, entry := range strings.Split(list, ",") {
		allowed := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry), "."))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return stepErrorf("%s isn't allowed: BROWSER_MCP_ALLOW_DOMAINS lists %s", host, list)
}

// confirmMode is on with BROWSER_MCP_CONFIRM=1: the commands that change a page
// report what they would do instead of doing it, so a person (or a supervising
// agent) can approve one at a time.
func confirmMode() bool {
	return os.Getenv("BROWSER_MCP_CONFIRM") == "1"
}

// writeCommands are the commands confirm mode holds back.
var writeCommands = map[string]bool{
	"click": true, "fill": true, "type": true, "press": true, "select": true,
	"check": true, "uncheck": true, "upload": true, "drag": true, "dialog": true,
}

// traceDir is where each call appends a JSON line, when set.
func traceDir() string { return strings.TrimSpace(os.Getenv("BROWSER_MCP_TRACE_DIR")) }

// callBudget is BROWSER_MCP_CALL_BUDGET_MS: the longest a whole call may run,
// however many steps it holds. Unset or invalid means no limit.
func callBudget() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("BROWSER_MCP_CALL_BUDGET_MS"))
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

// callTrace is one line of BROWSER_MCP_TRACE_DIR/calls.jsonl: what was asked,
// what each step said, and how long the call took.
type callTrace struct {
	At       string   `json:"at"`
	App      string   `json:"app"`
	Commands []string `json:"commands"`
	Output   []string `json:"output"`
	Failed   bool     `json:"failed"`
	MS       int64    `json:"ms"`
}

// writeCallTrace appends the call to the trace file, when tracing is on. The
// file holds page text, so it is written 0600 under a 0700 directory.
func writeCallTrace(dir, app string, lines []checkedLine, output []string, failed bool, started time.Time) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	record := callTrace{
		At:     started.UTC().Format(time.RFC3339Nano),
		App:    app,
		Output: output,
		Failed: failed,
		MS:     time.Since(started).Milliseconds(),
	}
	for _, line := range lines {
		record.Commands = append(record.Commands, line.echo)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}
