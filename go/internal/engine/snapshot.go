package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/NTbankey1/browser-mcp/go-daemon/internal/cdp"
)

// ariaNode is one accessibility node, shaped like Playwright's AI snapshot
// nodes so the same renderer and filters apply.
type ariaNode struct {
	Role     string
	Name     string
	Text     string
	Ref      string
	URL      string
	Props    []ariaProp
	Children []*ariaNode

	backendNodeID int
}

type ariaProp struct {
	Name  string
	Value any
}

type axValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type axNode struct {
	NodeID      string  `json:"nodeId"`
	Ignored     bool    `json:"ignored"`
	Role        axValue `json:"role"`
	Name        axValue `json:"name"`
	Description axValue `json:"description"`
	Value       axValue `json:"value"`
	Properties  []struct {
		Name  string  `json:"name"`
		Value axValue `json:"value"`
	} `json:"properties"`
	ChildIDs         []string `json:"childIds"`
	BackendDOMNodeID int      `json:"backendDOMNodeId"`
}

// propertyOrder keeps output stable between runs; a Go map would not.
var propertyOrder = []string{
	"checked", "disabled", "expanded", "haspopup", "invalid", "level",
	"modal", "multiline", "multiselectable", "pressed", "readonly",
	"required", "selected", "valuemin", "valuemax", "valuetext",
	"orientation", "url",
}

// takeSnapshot reads the page's accessibility tree and assigns refs in DOM
// order. The refs are what commands use as targets.
func takeSnapshot(ctx context.Context, tab *cdp.Tab, sessionID string) ([]*ariaNode, map[string]int, error) {
	_, _ = tab.SendOn(ctx, sessionID, "Accessibility.enable", nil)
	result, err := tab.SendOn(ctx, sessionID, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, nil, err
	}
	var reply struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*axNode, len(reply.Nodes))
	for i := range reply.Nodes {
		byID[reply.Nodes[i].NodeID] = &reply.Nodes[i]
	}
	visited := make(map[string]bool)
	var build func(node *axNode, depth int) []*ariaNode
	build = func(node *axNode, depth int) []*ariaNode {
		if node == nil || visited[node.NodeID] || depth > 200 {
			return nil
		}
		visited[node.NodeID] = true
		var children []*ariaNode
		for _, childID := range node.ChildIDs {
			children = append(children, build(byID[childID], depth+1)...)
		}
		if node.Ignored {
			// Ignored nodes are layout wrappers: keep their children in place
			return children
		}
		role := stringValue(node.Role)
		// The document root is not part of the page's content: Playwright's
		// aria snapshot starts at the body, and so does ours.
		if role == "" || role == "none" || role == "presentation" || role == "WebArea" || role == "RootWebArea" {
			return children
		}
		out := &ariaNode{
			Role:          role,
			Name:          stringValue(node.Name),
			Children:      children,
			backendNodeID: node.BackendDOMNodeID,
		}
		if role == "StaticText" {
			out.Role = "text"
			out.Text = stringValue(node.Name)
			if out.Text == "" {
				out.Text = stringValue(node.Value)
			}
		} else if value := stringValue(node.Value); value != "" && isValueRole(role) {
			out.Text = value
		}
		for _, property := range node.Properties {
			if !knownProperty(property.Name) {
				continue
			}
			value := decodedValue(property.Value)
			if value == nil || value == false || value == "false" {
				continue
			}
			if property.Name == "url" {
				out.URL, _ = value.(string)
				continue
			}
			out.Props = append(out.Props, ariaProp{Name: property.Name, Value: value})
		}
		sort.SliceStable(out.Props, func(i, j int) bool {
			return propertyRank(out.Props[i].Name) < propertyRank(out.Props[j].Name)
		})
		return []*ariaNode{out}
	}
	var roots []*ariaNode
	// The document root is the first node Chrome reports; anything unparented
	// after that is a popup or an iframe the tree roots separately.
	if len(reply.Nodes) > 0 {
		roots = append(roots, build(&reply.Nodes[0], 0)...)
	}
	for i := range reply.Nodes {
		if !visited[reply.Nodes[i].NodeID] {
			roots = append(roots, build(&reply.Nodes[i], 0)...)
		}
	}
	refs := make(map[string]int)
	assignRefs(roots, refs)
	return roots, refs, nil
}

func assignRefs(nodes []*ariaNode, refs map[string]int) {
	next := len(refs) + 1
	var walk func(list []*ariaNode)
	walk = func(list []*ariaNode) {
		for _, node := range list {
			if node.backendNodeID > 0 {
				node.Ref = fmt.Sprintf("e%d", next)
				refs[node.Ref] = node.backendNodeID
				next++
			}
			walk(node.Children)
		}
	}
	walk(nodes)
}

func stringValue(value axValue) string {
	if len(value.Value) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(value.Value, &text) == nil {
		return text
	}
	return ""
}

func decodedValue(value axValue) any {
	if len(value.Value) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(value.Value, &decoded) == nil {
		return decoded
	}
	return nil
}

func knownProperty(name string) bool {
	for _, known := range propertyOrder {
		if known == name {
			return true
		}
	}
	return false
}

func propertyRank(name string) int {
	for i, known := range propertyOrder {
		if known == name {
			return i
		}
	}
	return len(propertyOrder)
}

func isValueRole(role string) bool {
	switch role {
	case "textbox", "searchbox", "combobox", "spinbutton", "slider", "listbox":
		return true
	}
	return false
}

// Interactive roles `-i` keeps.
var interactiveRoles = setOf(
	"button", "link", "textbox", "searchbox", "checkbox", "radio", "combobox",
	"listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "tab",
	"switch", "slider", "spinbutton", "treeitem",
)

var landmarkRoles = setOf(
	"banner", "main", "navigation", "contentinfo", "complementary", "region",
	"form", "search", "dialog", "alertdialog",
)

var cellRoles = setOf("cell", "gridcell", "columnheader", "rowheader")

var layoutRoles = setOf(
	"generic", "none", "presentation", "rowgroup", "strong", "emphasis",
	"paragraph", "group", "table", "list", "article", "section",
)

var groupRoles = setOf("row", "listitem")

func setOf(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func isKept(node *ariaNode) bool {
	if interactiveRoles[node.Role] {
		return true
	}
	if landmarkRoles[node.Role] {
		return node.Name != "" || node.Role == "main"
	}
	if cellRoles[node.Role] {
		return node.Name != "" || node.Text != ""
	}
	return node.Role == "heading"
}

// InteractiveOnly drops what `-i` does not keep and unnamed wrappers, keeping
// the ancestors of kept nodes.
func InteractiveOnly(nodes []*ariaNode) []*ariaNode {
	var out []*ariaNode
	for _, node := range nodes {
		if node.Role == "text" {
			continue
		}
		children := InteractiveOnly(node.Children)
		kept := isKept(node)
		if !kept && len(children) == 0 {
			continue
		}
		wrapper := node.Name == "" &&
			(layoutRoles[node.Role] || cellRoles[node.Role] ||
				(groupRoles[node.Role] && len(children) == 1))
		if !kept && wrapper {
			out = append(out, children...)
			continue
		}
		copy := *node
		copy.Children = children
		if !kept {
			copy.Text = ""
			copy.Name = Truncate(copy.Name, 80)
		}
		out = append(out, &copy)
	}
	return out
}

// Render prints nodes as `- role "name" [ref=e1]: text` lines, two spaces per
// level. Nodes below depth are cut, marked with `…`.
func Render(nodes []*ariaNode, depth int) string {
	if depth <= 0 {
		depth = 1 << 30
	}
	var lines []string
	var walk func(list []*ariaNode, level int)
	walk = func(list []*ariaNode, level int) {
		for _, node := range list {
			children := node.Children
			cut := level+1 >= depth && len(children) > 0
			suffix := ""
			if cut {
				suffix = " …"
			} else if len(children) > 0 {
				suffix = ":"
			}
			lines = append(lines, strings.Repeat("  ", level)+"- "+nodeLine(node)+suffix)
			if !cut {
				walk(children, level+1)
			}
		}
	}
	walk(nodes, 0)
	return strings.Join(lines, "\n")
}

func nodeLine(node *ariaNode) string {
	if node.Role == "text" {
		return "text: " + node.Text
	}
	line := node.Role
	if node.Name != "" {
		line += " " + quote(node.Name)
	}
	for _, prop := range node.Props {
		if prop.Value == true {
			line += " [" + prop.Name + "]"
		} else {
			line += fmt.Sprintf(" [%s=%v]", prop.Name, prop.Value)
		}
	}
	if node.Ref != "" {
		line += " [ref=" + node.Ref + "]"
	}
	if node.Text != "" {
		line += ": " + node.Text
	}
	return line
}

// Find returns each node whose name, text or URL contains the text, with its
// ancestor path.
func Find(nodes []*ariaNode, query string) []string {
	needle := strings.ToLower(query)
	var matches []string
	var walk func(list []*ariaNode, path []string)
	walk = func(list []*ariaNode, path []string) {
		for _, node := range list {
			own := strings.Contains(strings.ToLower(node.Name), needle) ||
				strings.Contains(strings.ToLower(node.Text), needle) ||
				strings.Contains(strings.ToLower(node.URL), needle)
			textChild := false
			joined := ""
			for _, child := range node.Children {
				if child.Role == "text" && strings.Contains(strings.ToLower(child.Text), needle) {
					textChild = true
					if joined != "" {
						joined += " "
					}
					joined += child.Text
				}
			}
			if node.Role != "text" && (own || textChild) {
				shown := *node
				if textChild && node.Text == "" {
					shown.Text = joined
				}
				line := nodeLine(&shown)
				if len(path) > 0 {
					line = strings.Join(path, " > ") + " > " + line
				}
				matches = append(matches, line)
			}
			if label := pathLabel(node); label != "" {
				walk(node.Children, append(path, label))
			} else {
				walk(node.Children, path)
			}
		}
	}
	walk(nodes, nil)
	return matches
}

func pathLabel(node *ariaNode) string {
	if node.Name != "" {
		return node.Role + " " + quote(Truncate(node.Name, 40))
	}
	if landmarkRoles[node.Role] || node.Role == "dialog" {
		return node.Role
	}
	return ""
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}
