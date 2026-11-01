// Package collections contains small generic helpers for stable sets of
// comparable IDs. It deliberately does not abstract domain types.
package collections

import "slices"

// Dedup returns the input order with duplicate values removed.
func Dedup[T comparable](in []T) []T {
	seen := make(map[T]struct{}, len(in))
	out := make([]T, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// Union returns a's values followed by any new values from b.
func Union[T comparable](a, b []T) []T {
	out := make([]T, 0, len(a)+len(b))
	seen := make(map[T]struct{}, len(a)+len(b))
	for _, group := range [][]T{a, b} {
		for _, v := range group {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// SortedKeys returns map keys in deterministic order.
func SortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
