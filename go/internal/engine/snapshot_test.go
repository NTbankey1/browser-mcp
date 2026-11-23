package engine

import (
	"strings"
	"testing"
)

func sampleTree() []*ariaNode {
	return []*ariaNode{
		{
			Role: "generic", backendNodeID: 0,
			Children: []*ariaNode{
				{
					Role: "navigation", Name: "Main", backendNodeID: 0,
					Children: []*ariaNode{
						{Role: "link", Name: "Home", URL: "https://example.com/", Ref: "e1", backendNodeID: 1},
					},
				},
				{
					Role: "main", Name: "", backendNodeID: 0,
					Children: []*ariaNode{
						{Role: "heading", Name: "Welcome", Ref: "e2", backendNodeID: 2},
						{Role: "button", Name: "Submit", Ref: "e3", backendNodeID: 3},
						{Role: "text", Text: "Some copy"},
					},
				},
			},
		},
	}
}

func TestRender(t *testing.T) {
	text := Render(sampleTree(), 0)
	want := []string{
		`- link "Home" [ref=e1]`,
		`- navigation "Main":`,
		`- heading "Welcome" [ref=e2]`,
		`- button "Submit" [ref=e3]`,
		`- text: Some copy`,
	}
	for _, line := range want {
		if !strings.Contains(text, line) {
			t.Fatalf("rendered tree is missing %q:\n%s", line, text)
		}
	}
}

func TestRenderDepthCuts(t *testing.T) {
	text := Render(sampleTree(), 1)
	if !strings.Contains(text, "…") {
		t.Fatalf("depth did not cut:\n%s", text)
	}
	if strings.Contains(text, "Welcome") {
		t.Fatalf("depth 1 still printed a child:\n%s", text)
	}
}

func TestInteractiveOnly(t *testing.T) {
	filtered := InteractiveOnly(sampleTree())
	var roles []string
	var walk func(nodes []*ariaNode)
	walk = func(nodes []*ariaNode) {
		for _, node := range nodes {
			roles = append(roles, node.Role)
			walk(node.Children)
		}
	}
	walk(filtered)
	joined := strings.Join(roles, ",")
	for _, want := range []string{"navigation", "link", "main", "button", "heading"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("-i dropped %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "text") {
		t.Fatalf("-i kept a text node: %s", joined)
	}
}

func TestFind(t *testing.T) {
	matches := Find(sampleTree(), "submit")
	if len(matches) != 1 {
		t.Fatalf("got %d matches: %v", len(matches), matches)
	}
	if !strings.Contains(matches[0], `button "Submit"`) {
		t.Fatalf("match: %q", matches[0])
	}
	if !strings.Contains(matches[0], "main") {
		t.Fatalf("match lost its ancestor path: %q", matches[0])
	}
}

func TestCap(t *testing.T) {
	text := strings.Repeat("a", 100)
	capped := Cap(text, 10, "")
	if !strings.HasPrefix(capped, "aaaaaaaaaa") || !strings.Contains(capped, "cut at 10 of 100") {
		t.Fatalf("cap: %q", capped)
	}
}
