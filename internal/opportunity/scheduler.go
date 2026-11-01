package opportunity

import (
	"cmp"
	"slices"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/policy"
)

type Candidate struct {
	Program     domain.Program
	Status      domain.ProgramStatus
	Account     domain.Account
	Competition *domain.CompetitionSnapshot
}

type Options struct {
	TopK              int
	MaxPerPlatform    int
	MaxPerProgram     int
	Cooldown          time.Duration
	Existing          map[domain.ProgramID]time.Time
	ReserveEnterprise bool
	Now               time.Time
}

type Scheduled struct {
	Opportunity domain.Opportunity
	Program     domain.Program
	Reason      string
}

type Excluded struct {
	ProgramID domain.ProgramID
	Reason    string
}

type Result struct {
	Evaluated     int
	ResearchCount int
	HumanCount    int
	ExcludedCount int
	ResearchQueue []Scheduled
	HumanQueue    []Scheduled
	Excluded      []Excluded
}

func Schedule(engine Engine, candidates []Candidate, opts Options) Result {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	if opts.MaxPerPlatform <= 0 {
		opts.MaxPerPlatform = 2
	}
	if opts.MaxPerProgram <= 0 {
		opts.MaxPerProgram = 1
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 24 * time.Hour
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	var out Result
	var research []Scheduled
	for _, c := range candidates {
		status := c.Status
		if status == "" {
			status = c.Program.Status.Status
		}
		if status != domain.StatusRunning {
			out.Excluded = append(out.Excluded, Excluded{ProgramID: c.Program.ID, Reason: "PROGRAM_NOT_RUNNING"})
			continue
		}
		if last, ok := opts.Existing[c.Program.ID]; ok && opts.Now.Sub(last) < opts.Cooldown {
			out.Excluded = append(out.Excluded, Excluded{ProgramID: c.Program.ID, Reason: "COOLDOWN"})
			continue
		}
		opp, err := engine.Evaluate(Input{
			Program: c.Program, Status: status, Account: c.Account,
			Competition: c.Competition, Now: opts.Now,
		})
		if err != nil {
			out.Excluded = append(out.Excluded, Excluded{ProgramID: c.Program.ID, Reason: "NO_REWARD_DATA"})
			continue
		}
		out.Evaluated++
		s := Scheduled{Opportunity: opp, Program: c.Program}
		// Recompute eligibility from the account's current state instead of
		// trusting the Program row. Policy does the same at decision time, so a
		// gate resolved there cannot still look blocked here.
		elig, eligErr := policy.EvaluateEligibility(c.Account, c.Program, opts.Now, policy.DefaultPolicyVersion)
		switch {
		case !c.Account.Capability.CanSubmit:
			s.Reason = "ACCOUNT_CAPABILITY"
			out.HumanQueue = append(out.HumanQueue, s)
		case eligErr != nil || elig.Decision != domain.Eligible:
			s.Reason = "PROGRAM_GATE"
			out.HumanQueue = append(out.HumanQueue, s)
		case c.Program.Scope.Access != domain.ScopeAccessGranted || !c.Program.Scope.ContentsKnown:
			s.Reason = "SCOPE_GATED"
			out.HumanQueue = append(out.HumanQueue, s)
		default:
			s.Reason = "READY"
			research = append(research, s)
		}
	}
	sortScheduled(research)
	sortScheduled(out.HumanQueue)
	out.ResearchQueue = selectResearch(research, opts)
	out.ResearchCount = len(out.ResearchQueue)
	out.HumanCount = len(out.HumanQueue)
	out.ExcludedCount = len(out.Excluded)
	return out
}

func sortScheduled(s []Scheduled) {
	slices.SortStableFunc(s, func(a, b Scheduled) int {
		if c := cmp.Compare(score(b), score(a)); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Opportunity.Confidence, a.Opportunity.Confidence); c != 0 {
			return c
		}
		return cmp.Compare(a.Program.ID, b.Program.ID)
	})
}

func score(s Scheduled) float64 {
	if s.Opportunity.ExpectedReturnPerCost == nil {
		return 0
	}
	return *s.Opportunity.ExpectedReturnPerCost
}

func selectResearch(sorted []Scheduled, opts Options) []Scheduled {
	if len(sorted) == 0 || opts.TopK == 0 {
		return nil
	}
	var selected []Scheduled
	perPlatform := map[domain.Platform]int{}
	perProgram := map[domain.ProgramID]int{}
	for _, s := range sorted {
		if len(selected) >= opts.TopK {
			break
		}
		p := s.Program.Platform
		if perPlatform[p] >= opts.MaxPerPlatform || perProgram[s.Program.ID] >= opts.MaxPerProgram {
			continue
		}
		selected = append(selected, s)
		perPlatform[p]++
		perProgram[s.Program.ID]++
	}
	if opts.ReserveEnterprise && !hasEnterprise(selected) {
		for _, s := range sorted {
			if s.Program.Type != domain.TypeEnterpriseSRC {
				continue
			}
			if containsProgram(selected, s.Program.ID) {
				continue
			}
			p := s.Program.Platform
			if len(selected) < opts.TopK {
				if perPlatform[p] >= opts.MaxPerPlatform {
					continue
				}
				selected = append(selected, s)
				perPlatform[p]++
				break
			}
			// Replacing the last slot must still respect the platform quota;
			// otherwise the reserved enterprise program pushes a platform over
			// its configured cap.
			last := selected[len(selected)-1]
			lp := last.Program.Platform
			if lp != p && perPlatform[p]+1 > opts.MaxPerPlatform {
				continue
			}
			selected[len(selected)-1] = s
			perPlatform[lp]--
			perPlatform[p]++
			break
		}
	}
	sortScheduled(selected)
	return selected
}

func hasEnterprise(s []Scheduled) bool {
	for _, x := range s {
		if x.Program.Type == domain.TypeEnterpriseSRC {
			return true
		}
	}
	return false
}

func containsProgram(s []Scheduled, id domain.ProgramID) bool {
	for _, x := range s {
		if x.Program.ID == id {
			return true
		}
	}
	return false
}
