package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PackageVersion returns the version in package.json next to the binary
// (bin/../package.json, or package.json beside the binary), or fallback when
// neither is readable. Reading it at startup keeps a Go binary built from an
// older checkout in step with the installed package after a version bump.
func PackageVersion(executable, fallback string) string {
	dir := filepath.Dir(executable)
	for _, candidate := range []string{
		filepath.Join(dir, "..", "package.json"),
		filepath.Join(dir, "package.json"),
	} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var pkg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Version != "" {
			return pkg.Version
		}
	}
	return fallback
}
