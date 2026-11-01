package domain

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkScopeCheckAsset(b *testing.B) {
	assets := make([]Asset, 0, 256)
	for i := 0; i < 256; i++ {
		assets = append(assets, Asset{ID: AssetID(fmt.Sprintf("a%d", i)), Value: fmt.Sprintf("*.s%d.example.com", i), InScope: true})
	}
	s := Scope{
		Visibility: ScopePublic, Access: ScopeAccessGranted, ContentsKnown: true,
		Include: assets, ObservedAt: time.Unix(0, 0),
	}
	target := Asset{ID: "target", Value: "www.s255.example.com", InScope: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if s.CheckAsset(target) != ScopeMatchIncluded {
			b.Fatal("unexpected match")
		}
	}
}
