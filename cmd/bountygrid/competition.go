package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/evidence"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type competitionOptions struct {
	File    string
	Persist bool
	RawDir  string
}

type competitionInput struct {
	Platform            string            `json:"platform"`
	Scope               string            `json:"scope"`
	ObservedAt          time.Time         `json:"observed_at"`
	ActiveResearchers   *int              `json:"active_researchers,omitempty"`
	RewardedResearchers *int              `json:"rewarded_researchers,omitempty"`
	Top1Share           *float64          `json:"top1_share,omitempty"`
	Top3Share           *float64          `json:"top3_share,omitempty"`
	Top10Share          *float64          `json:"top10_share,omitempty"`
	DuplicatePressure   *float64          `json:"duplicate_pressure,omitempty"`
	Saturation          *float64          `json:"saturation,omitempty"`
	RewardPool          *domain.Money     `json:"reward_pool,omitempty"`
	Confidence          domain.Confidence `json:"confidence"`
	EvidenceURL         string            `json:"evidence_url"`
	EvidenceTitle       string            `json:"evidence_title"`
	EvidenceText        string            `json:"evidence_text"`
}

type competitionStore interface {
	PutEvidence(ctx context.Context, e domain.Evidence) error
	PutCompetitionSnapshot(ctx context.Context, s domain.CompetitionSnapshot) error
}

func parseCompetitionOptions(args []string) (competitionOptions, error) {
	var o competitionOptions
	fs := flag.NewFlagSet("competition", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.File, "file", "", "JSON competition snapshot with evidence text")
	fs.BoolVar(&o.Persist, "persist", false, "write Evidence and the CompetitionSnapshot to PostgreSQL")
	fs.StringVar(&o.RawDir, "raw-dir", ".evidence/competition", "local directory for redacted evidence")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.File == "" {
		return o, fmt.Errorf("competition: -file is required")
	}
	return o, nil
}

func runCompetition(ctx context.Context, args []string) error {
	opts, err := parseCompetitionOptions(args)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(opts.File)
	if err != nil {
		return fmt.Errorf("competition: read file: %w", err)
	}
	var in competitionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("competition: decode: %w", err)
	}
	now := time.Now().UTC()
	snap, ev, err := buildCompetitionSnapshot(in, now, opts.RawDir)
	if err != nil {
		return err
	}
	if opts.Persist {
		d, err := dsn()
		if err != nil {
			return err
		}
		pool, err := postgres.Open(ctx, d)
		if err != nil {
			return err
		}
		defer pool.Close()
		repo := postgres.NewRepo(pool)
		if err := repo.PutEvidence(ctx, ev); err != nil {
			return err
		}
		if err := repo.PutCompetitionSnapshot(ctx, snap); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{
		"id": snap.ID, "platform": snap.Platform, "scope": snap.Scope,
		"observed_at": snap.ObservedAt, "valid_until": snap.ValidUntil,
		"confidence": snap.Confidence, "evidence": ev.ID, "persisted": opts.Persist,
	})
}

func buildCompetitionSnapshot(in competitionInput, now time.Time, rawDir string) (domain.CompetitionSnapshot, domain.Evidence, error) {
	platform := domain.Platform(in.Platform)
	if !platform.Valid() {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: invalid platform %q", in.Platform)
	}
	if strings.TrimSpace(in.Scope) == "" {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: scope is empty")
	}
	if in.ObservedAt.IsZero() {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: observed_at is zero")
	}
	if !in.Confidence.Valid() {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: confidence out of range")
	}
	for name, v := range map[string]*float64{
		"top1_share": in.Top1Share, "top3_share": in.Top3Share, "top10_share": in.Top10Share,
		"duplicate_pressure": in.DuplicatePressure, "saturation": in.Saturation,
	} {
		if v != nil && (*v < 0 || *v > 1) {
			return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: %s out of range", name)
		}
	}
	if strings.TrimSpace(in.EvidenceURL) == "" || strings.TrimSpace(in.EvidenceText) == "" {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: evidence_url/evidence_text required")
	}
	if rawDir == "" {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: raw dir is required")
	}
	evID := domain.EvidenceID(fmt.Sprintf("competition/%s/%s/%d", in.Platform, in.Scope, now.UnixNano()))
	rawRef := filepath.Join(rawDir, compSafeName(string(evID))+".txt")
	ev := domain.Evidence{
		ID: evID, Platform: platform, SourceType: domain.SourceAnnouncement,
		Transport: domain.TransportManualImport, URL: in.EvidenceURL,
		PageTitle: in.EvidenceTitle, CapturedAt: now, RawRef: rawRef,
	}
	prepared, redacted, _, err := evidence.Prepare(ev, in.EvidenceText)
	if err != nil {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, err
	}
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, err
	}
	if err := os.WriteFile(rawRef, []byte(redacted), 0o600); err != nil {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, err
	}
	observedAt := in.ObservedAt.UTC()
	snap := domain.CompetitionSnapshot{
		ID:       fmt.Sprintf("comp/%s/%s/%s", in.Platform, in.Scope, observedAt.Format("20060102T150405.000000000")),
		Platform: platform, Scope: in.Scope, ObservedAt: observedAt,
		// Competition data is only trusted for 30 days; the Opportunity Engine
		// additionally decays confidence after 7 days.
		ValidUntil:        observedAt.Add(30 * 24 * time.Hour),
		ActiveResearchers: in.ActiveResearchers, RewardedResearchers: in.RewardedResearchers,
		Top1Share: in.Top1Share, Top3Share: in.Top3Share, Top10Share: in.Top10Share,
		DuplicatePressure: in.DuplicatePressure, Saturation: in.Saturation,
		RewardPool: in.RewardPool, Confidence: in.Confidence,
		Evidence: []domain.EvidenceID{prepared.ID},
	}
	if err := snap.Validate(); err != nil {
		return domain.CompetitionSnapshot{}, domain.Evidence{}, fmt.Errorf("competition: %w", err)
	}
	return snap, prepared, nil
}

func compSafeName(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(s)
}
