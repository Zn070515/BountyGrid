package domain

import (
	"testing"
	"time"
)

func FuzzScopeCheckAsset(f *testing.F) {
	f.Add("www.example.com", "*.example.com")
	f.Add("example.com", "*.example.com")
	f.Add("a.example.com", "a.example.com")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, value, pattern string) {
		s := Scope{
			Visibility: ScopePublic, Access: ScopeAccessGranted, ContentsKnown: true,
			Include:    []Asset{{ID: "a1", Value: pattern, InScope: true}},
			Exclude:    []Asset{{ID: "a2", Value: value, InScope: false}},
			ObservedAt: time.Unix(0, 0),
		}
		_ = s.CheckAsset(Asset{ID: "a1", Value: value, InScope: true})
	})
}

func FuzzCanonicalParametersHash(f *testing.F) {
	f.Add([]byte(`{"a":1,"b":[2,3]}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = CanonicalParametersHash(b)
		_ = ParametersHash(b)
	})
}
