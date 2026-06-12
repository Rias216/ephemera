// Package util contains small dependency-free helpers shared across Ephemera.
package util

import (
	"sort"
	"strings"
)

// Contains reports whether target occurs in values.
func Contains[T comparable](values []T, target T) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// DedupStrings removes empty and duplicate strings while preserving first-use order.
func DedupStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// UniqueSortedStrings removes empty and duplicate strings and sorts the result.
func UniqueSortedStrings(values []string) []string {
	out := DedupStrings(values)
	sort.Strings(out)
	return out
}
