package engine

import (
	"strings"
	"testing"
)

func TestBrowserToolDefinition(t *testing.T) {
	tool := browserTool(nil)
	if tool.Name != "browser" {
		t.Fatalf("tool name %q", tool.Name)
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	if properties["commands"] == nil {
		t.Fatal("the tool has no commands input")
	}
	required := tool.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "commands" {
		t.Fatalf("required: %v", required)
	}
	for _, needle := range []string{"goto <url>", "snapshot", `find "<text>"`, "Do many steps per call"} {
		if !strings.Contains(tool.Description, needle) {
			t.Fatalf("description is missing %q:\n%s", needle, tool.Description)
		}
	}
	// Every turn pays for this definition again; keep it small.
	if len(tool.Description) > 1500 {
		t.Fatalf("description is %d characters; keep it under 1500", len(tool.Description))
	}
	withCdp := browserTool([]string{"cdp"})
	if !strings.Contains(withCdp.Description, "cdp") {
		t.Fatal("a session with --caps cdp does not see cdp in the description")
	}
}

func TestInstructionsMentionTheWorkflow(t *testing.T) {
	for _, needle := range []string{"Act in batches", "snapshot -i", "Re-snapshot"} {
		if !strings.Contains(instructions, needle) {
			t.Fatalf("instructions are missing %q", needle)
		}
	}
}
