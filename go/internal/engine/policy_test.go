package engine

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct{ message, want string }{
		{"waiting for locator('#go') to be visible", CodeNoElement},
		{"Nothing matches \"#go\"", CodeNoElement},
		{"There's no tab 7. Run `tab list`", CodeNoElement},
		{"context deadline exceeded", CodeTimeout},
		{"the browser didn't answer in time", CodeTimeout},
		{`confirm dialog "Continue?" opened. Answer it with ` + "`dialog accept`", CodeDialog},
		{"the browser disconnected from Browser MCP", CodeDisconnected},
		{"Browser MCP doesn't allow Network.getCookies: it reaches other sites", CodeRefused},
		{"evil.test isn't allowed: BROWSER_MCP_ALLOW_DOMAINS lists a.test", CodeRefused},
		{"HTTP 404 for https://example.com/x", CodeHTTP},
		{"something else entirely", ""},
	}
	for _, c := range cases {
		if got := classify(c.message); got != c.want {
			t.Errorf("classify(%q) = %q, want %q", c.message, got, c.want)
		}
	}
}

func TestRedactHeader(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer secret-token": "Authorization: [redacted]",
		"cookie: session=abc":                "cookie: [redacted]",
		"Set-Cookie: a=1; HttpOnly":          "Set-Cookie: [redacted]",
		"content-type: text/html":            "content-type: text/html",
		"no colon here":                      "no colon here",
	}
	for in, want := range cases {
		if got := redactHeader(in); got != want {
			t.Errorf("redactHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckAllowed(t *testing.T) {
	t.Setenv("BROWSER_MCP_ALLOW_DOMAINS", "")
	if err := checkAllowed("https://anywhere.example/x"); err != nil {
		t.Fatalf("no allowlist should allow everything: %v", err)
	}

	t.Setenv("BROWSER_MCP_ALLOW_DOMAINS", "example.com, .test")
	for _, url := range []string{
		"https://example.com/a",
		"https://sub.example.com/a",
		"https://shop.test/a",
		"/relative/path",
	} {
		if err := checkAllowed(url); err != nil {
			t.Errorf("checkAllowed(%q) refused: %v", url, err)
		}
	}
	for _, url := range []string{"https://evil.example/a", "https://notexample.com/a"} {
		err := checkAllowed(url)
		if err == nil {
			t.Errorf("checkAllowed(%q) should be refused", url)
			continue
		}
		if code := classify(err.Error()); code != CodeRefused {
			t.Errorf("checkAllowed(%q) code = %q, want %q", url, code, CodeRefused)
		}
	}
}

func TestConfirmModeAndWrites(t *testing.T) {
	t.Setenv("BROWSER_MCP_CONFIRM", "1")
	if !confirmMode() {
		t.Fatal("BROWSER_MCP_CONFIRM=1 should turn confirm mode on")
	}
	t.Setenv("BROWSER_MCP_CONFIRM", "")
	if confirmMode() {
		t.Fatal("confirm mode must be off by default")
	}
	for _, name := range []string{"click", "fill", "type", "press", "upload", "dialog"} {
		if !writeCommands[name] {
			t.Errorf("%q should be held back in confirm mode", name)
		}
	}
	for _, name := range []string{"get", "find", "snapshot", "wait", "eval"} {
		if writeCommands[name] {
			t.Errorf("%q must not be held back", name)
		}
	}
	if !strings.Contains(ErrorMessageWithCode(stepErrorf("HTTP 500 for x")), "[http]") {
		t.Error("a step line should carry the code")
	}
}
