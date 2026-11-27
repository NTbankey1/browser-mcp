package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// sessionSnapshot is what `session save` writes: the tabs this AI app is using,
// their URLs, and which one was current.
type sessionSnapshot struct {
	SavedAt string   `json:"savedAt"`
	URLs    []string `json:"urls"`
	Active  int      `json:"active"`
}

func runSession(ctx context.Context, c *stepContext) error {
	action, err := positional(c.args, 0, "save|restore")
	if err != nil {
		return err
	}
	file, err := positional(c.args, 1, "<file>")
	if err != nil {
		return err
	}
	switch action {
	case "save":
		return saveSession(ctx, c, file)
	case "restore":
		return restoreSession(ctx, c, file)
	default:
		return stepErrorf("session takes save or restore")
	}
}

// saveSession writes the app's tabs in a stable order, so two saves of the same
// tabs produce the same file.
func saveSession(ctx context.Context, c *stepContext, file string) error {
	engine := c.engine
	engine.mu.Lock()
	ids := make([]int, 0, len(c.session.tabs))
	for id := range c.session.tabs {
		ids = append(ids, id)
	}
	engine.mu.Unlock()
	sort.Ints(ids)

	current := engine.currentTab(c.session)
	snapshot := sessionSnapshot{SavedAt: time.Now().UTC().Format(time.RFC3339), Active: 0}
	for _, id := range ids {
		tab, found := engine.browsers.FindTab(id)
		if !found {
			continue
		}
		url := tab.URL
		if live, err := engine.quickURL(ctx, id); err == nil && live != "" {
			url = live
		}
		if id == current {
			snapshot.Active = len(snapshot.URLs)
		}
		snapshot.URLs = append(snapshot.URLs, url)
	}
	if len(snapshot.URLs) == 0 {
		return stepErrorf("There are no tabs to save. Open one with `goto <url>` or `tab new`")
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := saveFile(ctx, c.session.IO, file, append(encoded, '\n'), false); err != nil {
		return err
	}
	c.Note(fmt.Sprintf("saved %d tabs to %s", len(snapshot.URLs), file))
	return nil
}

// restoreSession opens the saved URLs as this app's tabs and leaves the tab that
// was current current again. It honours the allowlist, so a saved file cannot
// take the browser somewhere `goto` would refuse.
func restoreSession(ctx context.Context, c *stepContext, file string) error {
	_, _, raw, err := c.session.IO.ReadFile(ctx, file)
	if err != nil {
		return stepErrorf("%s", ErrorMessage(err))
	}
	var snapshot sessionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return stepErrorf("%s isn't a session snapshot: %s", file, ErrorMessage(err))
	}
	if len(snapshot.URLs) == 0 {
		return stepErrorf("%s lists no tabs", file)
	}
	browserID := c.engine.tabBrowser(c.session)
	opened := make([]int, 0, len(snapshot.URLs))
	for _, url := range snapshot.URLs {
		if err := checkAllowed(url); err != nil {
			return err
		}
		tabID, err := c.engine.browsers.CreateTab(ctx, url, c.session.App, browserID)
		if err != nil {
			return err
		}
		c.engine.adopt(c.session, tabID, true)
		opened = append(opened, tabID)
	}
	if snapshot.Active >= 0 && snapshot.Active < len(opened) {
		c.engine.adopt(c.session, opened[snapshot.Active], true)
	}
	c.Note(fmt.Sprintf("opened %d tabs; current is %d", len(opened), opened[minInt(snapshot.Active, len(opened)-1)]))
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
