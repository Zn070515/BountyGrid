package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/audit"
	"github.com/Zn070515/BountyGrid/internal/domain"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

type revokeOptions struct {
	Program string
	Reason  string
}

func parseRevokeOptions(args []string) (revokeOptions, error) {
	var o revokeOptions
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Program, "program", "", "program ID whose outstanding execution grants to revoke")
	fs.StringVar(&o.Reason, "reason", "", "operator reason recorded in the audit event")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.Program = strings.TrimSpace(o.Program)
	o.Reason = strings.TrimSpace(o.Reason)
	if o.Program == "" {
		return o, errors.New("revoke: -program is required")
	}
	return o, nil
}

// runRevoke bumps the durable per-program authorization epoch. Every execution
// grant stamped with an older epoch is rejected by GrantPolicy and Egress on
// its next request, including after a process restart. The bump is audited.
func runRevoke(ctx context.Context, args []string) error {
	opts, err := parseRevokeOptions(args)
	if err != nil {
		return err
	}
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

	epoch, err := repo.BumpRevocationEpoch(ctx, domain.ProgramID(opts.Program))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Program string `json:"program"`
		Epoch   uint64 `json:"epoch"`
		Reason  string `json:"reason,omitempty"`
	}{Program: opts.Program, Epoch: epoch, Reason: opts.Reason}); err != nil {
		return err
	}

	payload, err := json.Marshal(struct {
		Epoch  uint64 `json:"epoch"`
		Reason string `json:"reason,omitempty"`
	}{Epoch: epoch, Reason: opts.Reason})
	if err != nil {
		return err
	}
	if _, err := postgres.NewAuditStore(pool).Append(ctx, audit.Event{
		Type:       "grant.revoked",
		EntityType: "program",
		EntityID:   opts.Program,
		ActorKind:  string(domain.ActorHuman),
		ActorID:    "cli",
		Payload:    payload,
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("revoke: epoch bumped to %d but audit failed: %w", epoch, err)
	}
	return nil
}
