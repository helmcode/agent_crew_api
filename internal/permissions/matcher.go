package permissions

import (
	"path/filepath"
	"strings"
)

// MatchPattern performs glob-style matching of a pattern against a value.
// It supports "*" as a wildcard that matches any sequence of characters.
// Uses an iterative algorithm to avoid ReDoS with multiple wildcards.
func MatchPattern(pattern, value string) bool {
	// Iterative matching with backtracking positions.
	// pi = pattern index, vi = value index.
	// starIdx / matchIdx track the last '*' position for backtracking.
	pi, vi := 0, 0
	starIdx, matchIdx := -1, 0

	for vi < len(value) {
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = vi
			pi++
		} else if pi < len(pattern) && pattern[pi] == value[vi] {
			pi++
			vi++
		} else if starIdx != -1 {
			// Backtrack: let the last '*' consume one more character.
			pi = starIdx + 1
			matchIdx++
			vi = matchIdx
		} else {
			return false
		}
	}

	// Consume remaining '*' characters in pattern.
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern)
}

// IsPathInScope checks whether path is located under the scope directory.
// It resolves symlinks and ".." traversals to prevent escape attacks.
func IsPathInScope(path, scope string) bool {
	if path == "" || scope == "" {
		return false
	}

	// Resolve symlinks to get real paths. Fall back to Clean if the path
	// does not exist yet (e.g., a file about to be created).
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		realPath = filepath.Clean(path)
	}

	realScope, err := filepath.EvalSymlinks(scope)
	if err != nil {
		realScope = filepath.Clean(scope)
	}

	// The path must be equal to the scope or be a child of it.
	if realPath == realScope {
		return true
	}

	// Ensure the scope ends with separator for prefix matching.
	scopePrefix := realScope + string(filepath.Separator)
	return strings.HasPrefix(realPath, scopePrefix)
}
