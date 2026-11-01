package postgres

import (
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

var revocationNow = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

func revocationProgram(id domain.ProgramID) domain.Program {
	return domain.Program{
		ID: id, Platform: domain.PlatformVulbox, Name: "it", Type: domain.TypeBrandSRC,
		Status: domain.ProgramState{
			Status: domain.StatusRunning, ObservedAt: revocationNow, ValidUntil: revocationNow.Add(time.Hour), Confidence: 1,
		},
		Eligibility: domain.EligibilityState{Decision: domain.Eligible, EvaluatedAt: revocationNow, PolicyVersion: "it"},
		Scope: domain.Scope{
			Visibility: domain.ScopePublic, Access: domain.ScopeAccessGranted,
			ContentsKnown: true, ObservedAt: revocationNow,
		},
		Reward: domain.RewardPolicy{TaxBasis: domain.TaxUnknown},
		Automation: domain.AutomationPolicy{
			AIAnalysis: domain.CapUnknown, AutonomousAgent: domain.CapUnknown,
			ActiveHTTP: domain.CapUnknown, BrowserAutomation: domain.CapUnknown,
			AutomatedScanning: domain.CapUnknown, DirectoryEnumeration: domain.CapUnknown,
			SubdomainDiscovery: domain.CapUnknown, HighConcurrency: domain.CapUnknown,
			BruteForce: domain.CapUnknown, CredentialStuffing: domain.CapUnknown,
			FileUpload: domain.CapUnknown, StateMutation: domain.CapUnknown,
			RCEVerification: domain.CapUnknown, SensitiveDataRead: domain.CapUnknown,
			InternalNetwork: domain.CapUnknown, LateralMovement: domain.CapUnknown,
			DoS: domain.CapUnknown,
		},
		ObservedAt: revocationNow,
	}
}

func TestProgramSupersedesAuthority(t *testing.T) {
	base := revocationProgram("p")
	cases := []struct {
		name string
		next func(domain.Program) domain.Program
		want bool
	}{
		{"identical", func(p domain.Program) domain.Program { return p }, false},
		{"newer observation only", func(p domain.Program) domain.Program {
			p.ObservedAt = revocationNow.Add(time.Minute)
			p.Status.ObservedAt = revocationNow.Add(time.Minute)
			p.Status.ValidUntil = revocationNow.Add(2 * time.Hour)
			return p
		}, false},
		{"paused", func(p domain.Program) domain.Program {
			p.Status.Status = domain.StatusPaused
			return p
		}, true},
		{"unknown status", func(p domain.Program) domain.Program {
			p.Status.Status = domain.StatusUnknown
			return p
		}, true},
		{"ended", func(p domain.Program) domain.Program {
			p.Status.Status = domain.StatusEnded
			return p
		}, true},
		{"resumed is not a revocation", func(p domain.Program) domain.Program {
			p.Status.Status = domain.StatusRunning
			return p
		}, false},
		{"scope content change", func(p domain.Program) domain.Program {
			p.Scope.Include = []domain.Asset{{ID: "asset-1", Value: "example.test"}}
			return p
		}, true},
		{"automation change", func(p domain.Program) domain.Program {
			p.Automation.ActiveHTTP = domain.CapConditional
			return p
		}, true},
		{"platform change", func(p domain.Program) domain.Program {
			p.Platform = domain.PlatformButian
			return p
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := programSupersedesAuthority(base, tc.next(base)); got != tc.want {
				t.Fatalf("supersedes = %t, want %t", got, tc.want)
			}
		})
	}
}
