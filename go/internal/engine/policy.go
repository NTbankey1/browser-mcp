package engine

import (
	"net/url"
	"os"
	"strings"
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
