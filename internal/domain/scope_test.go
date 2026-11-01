package domain

import "testing"

func grantedScope(include ...Asset) Scope {
	return Scope{
		Visibility:    ScopePublic,
		Access:        ScopeAccessGranted,
		ContentsKnown: true,
		Include:       include,
		ObservedAt:    now(),
	}
}

func TestCheckAssetRequiresGrantedAccess(t *testing.T) {
	s := Scope{Visibility: ScopeGatedBySignup, Access: ScopeAccessUnknown, ObservedAt: now()}
	if got := s.CheckAsset(Asset{ID: "a1", Value: "example.com", InScope: true}); got != ScopeMatchUnknown {
		t.Fatalf("CheckAsset = %s, want UNKNOWN", got)
	}
}

func TestCheckAssetFailsClosedOnEmptyInclude(t *testing.T) {
	// ContentsKnown with no included assets means the scope is known to contain
	// nothing executable; it must never become "everything is in scope".
	s := grantedScope()
	if got := s.CheckAsset(Asset{ID: "a1", Value: "example.com", InScope: true}); got != ScopeMatchExcluded {
		t.Fatalf("CheckAsset = %s, want EXCLUDED", got)
	}
}

func TestCheckAssetWildcardDoesNotMatchApex(t *testing.T) {
	s := grantedScope(Asset{ID: "w1", Value: "*.example.com"})
	if got := s.CheckAsset(Asset{ID: "a1", Value: "www.example.com", InScope: true}); got != ScopeMatchIncluded {
		t.Fatalf("subdomain = %s, want INCLUDED", got)
	}
	if got := s.CheckAsset(Asset{ID: "a2", Value: "example.com", InScope: true}); got != ScopeMatchExcluded {
		t.Fatalf("apex = %s, want EXCLUDED", got)
	}
}

func TestCheckAssetConditionalAndExcludePrecedence(t *testing.T) {
	s := grantedScope(Asset{ID: "a1", Value: "example.com"})
	s.Conditional = []Asset{{ID: "a1", Value: "example.com"}}
	if got := s.CheckAsset(Asset{ID: "a1", Value: "example.com", InScope: true}); got != ScopeMatchConditional {
		t.Fatalf("conditional = %s, want CONDITIONAL", got)
	}
	s.Exclude = []Asset{{ID: "a1", Value: "example.com"}}
	if got := s.CheckAsset(Asset{ID: "a1", Value: "example.com", InScope: true}); got != ScopeMatchExcluded {
		t.Fatalf("excluded = %s, want EXCLUDED", got)
	}
}

func TestResolveAssetReturnsStoredValuePrecedence(t *testing.T) {
	s := grantedScope(Asset{ID: "a1", Value: "include.example.com", InScope: true})
	got, match, ok := s.ResolveAsset("a1")
	if !ok || match != ScopeMatchIncluded || got.Value != "include.example.com" {
		t.Fatalf("include resolve = %+v %s %v", got, match, ok)
	}
	if _, _, ok := s.ResolveAsset("missing"); ok {
		t.Fatal("unknown id must not resolve")
	}

	s.Conditional = []Asset{{ID: "a1", Value: "conditional.example.com", InScope: true}}
	if got, match, _ := s.ResolveAsset("a1"); match != ScopeMatchConditional || got.Value != "conditional.example.com" {
		t.Fatalf("conditional must win over include: %+v %s", got, match)
	}
	s.Exclude = []Asset{{ID: "a1", Value: "exclude.example.com", InScope: false}}
	if got, match, _ := s.ResolveAsset("a1"); match != ScopeMatchExcluded || got.Value != "exclude.example.com" {
		t.Fatalf("exclude must win: %+v %s", got, match)
	}
}

func TestCheckAssetHonorsInScopeFlag(t *testing.T) {
	s := grantedScope(Asset{ID: "a1", Value: "example.com"})
	if got := s.CheckAsset(Asset{ID: "a1", Value: "example.com", InScope: false}); got != ScopeMatchExcluded {
		t.Fatalf("CheckAsset = %s, want EXCLUDED", got)
	}
}
