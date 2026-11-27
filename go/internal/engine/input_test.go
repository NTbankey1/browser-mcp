package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandSuggestions(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		// A word of a multi-word command, not a command of its own
		{"list", []string{"tab list", "browser list"}},
		{"new", []string{"tab new"}},
		{"use", []string{"browser use"}},
		// Prefixes
		{"snap", []string{"snapshot"}},
		{"fil", []string{"fill"}},
		// One or two edits away
		{"clik", []string{"click"}},
		{"clcik", []string{"click"}},
		// A short word two edits away is not a typo worth guessing
		{"gtoo", nil},
		// Nothing close
		{"nope", nil},
		{"", nil},
	}
	for _, test := range cases {
		if got := commandSuggestions(test.name); !reflect.DeepEqual(got, test.want) {
			t.Errorf("commandSuggestions(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestUnknownCommandHint(t *testing.T) {
	if hint := unknownCommandHint("list"); !strings.Contains(hint, "`tab list`") || !strings.Contains(hint, "`browser list`") {
		t.Fatalf("hint for list: %q", hint)
	}
	if hint := unknownCommandHint("nope"); !strings.Contains(hint, "help") || strings.Contains(hint, "Did you mean") {
		t.Fatalf("hint for nope: %q", hint)
	}
}

func TestCommandsArgument(t *testing.T) {
	input, failure := commandsArgument(map[string]any{"commands": "goto https://example.com"})
	if failure != "" || input != "goto https://example.com" {
		t.Fatalf("a commands string: %q %q", input, failure)
	}

	// An array of lines works too, as the Node engine's does
	input, failure = commandsArgument(map[string]any{"commands": []any{"help", "tab list"}})
	if failure != "" || input != "help\ntab list" {
		t.Fatalf("an array of lines: %q %q", input, failure)
	}
	input, failure = commandsArgument(map[string]any{"commands": []string{"tab list"}})
	if failure != "" || input != "tab list" {
		t.Fatalf("a []string: %q %q", input, failure)
	}

	// A near-miss key is named, and the right one suggested
	_, failure = commandsArgument(map[string]any{"command": "help"})
	if !strings.Contains(failure, "`command`") || !strings.Contains(failure, "Did you mean `commands`?") {
		t.Fatalf("a wrong key: %q", failure)
	}

	// A wrong type says which
	_, failure = commandsArgument(map[string]any{"commands": 5})
	if !strings.Contains(failure, "`commands` is a number") {
		t.Fatalf("a number: %q", failure)
	}
	_, failure = commandsArgument(map[string]any{"commands": nil})
	if !strings.Contains(failure, "`commands` is null") {
		t.Fatalf("null: %q", failure)
	}
	_, failure = commandsArgument(map[string]any{"commands": []any{"help", 5}})
	if !strings.Contains(failure, "array with something") {
		t.Fatalf("a mixed array: %q", failure)
	}

	// No argument at all
	_, failure = commandsArgument(nil)
	if !strings.Contains(failure, "No arguments") || !strings.Contains(failure, "`commands`") {
		t.Fatalf("no arguments: %q", failure)
	}
}

func TestWaitFailureNamesThePageState(t *testing.T) {
	base := `The text "Message ChatGPT" didn't appear within 10 s`
	if got := waitFailure(base, pageState{}, "", nil); got != base {
		t.Fatalf("a page with nothing to say: %q", got)
	}
	got := waitFailure(base, pageState{
		ReadyState: "loading",
		URL:        "https://chatgpt.com/",
		Sample:     "Where should we begin?",
	}, "https://chatgpt.com/", []string{"it is in the page's HTML but not in its visible text"})
	for _, needle := range []string{base, "the page's HTML", "still loading (readyState loading)", `shows "Where should we begin?"`} {
		if !strings.Contains(got, needle) {
			t.Fatalf("the failure is missing %q: %q", needle, got)
		}
	}

	// A navigation while waiting is what explains most timeouts on a slow site
	got = waitFailure(base, pageState{ReadyState: "complete", URL: "https://chatgpt.com/auth/login"}, "https://chatgpt.com/", nil)
	if !strings.Contains(got, "navigated to /auth/login") {
		t.Fatalf("a navigation: %q", got)
	}
}
