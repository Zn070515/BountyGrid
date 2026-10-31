package domain

import "strings"

// CheckAsset returns whether an asset is included, conditional, excluded or
// unknown for this scope. Access must be GRANTED and contents known; otherwise
// the answer is UNKNOWN, never "included".
func (s Scope) CheckAsset(a Asset) ScopeMatch {
	if s.Access != ScopeAccessGranted || !s.ContentsKnown {
		return ScopeMatchUnknown
	}
	if !a.InScope {
		return ScopeMatchExcluded
	}
	for _, ex := range s.Exclude {
		if assetMatches(ex, a) {
			return ScopeMatchExcluded
		}
	}
	for _, c := range s.Conditional {
		if assetMatches(c, a) {
			return ScopeMatchConditional
		}
	}
	if len(s.Include) == 0 {
		return ScopeMatchIncluded
	}
	for _, inc := range s.Include {
		if assetMatches(inc, a) {
			return ScopeMatchIncluded
		}
	}
	return ScopeMatchExcluded
}

func assetMatches(pattern, a Asset) bool {
	if pattern.ID != "" && pattern.ID == a.ID {
		return true
	}
	if pattern.Value == "" || a.Value == "" {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(pattern.Value))
	v := strings.ToLower(strings.TrimSpace(a.Value))
	if p == v {
		return true
	}
	if strings.HasPrefix(p, "*.") {
		suffix := p[1:] // ".example.com"
		apex := strings.TrimPrefix(p, "*.")
		return strings.HasSuffix(v, suffix) || v == apex
	}
	return false
}
