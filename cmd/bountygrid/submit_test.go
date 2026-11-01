package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Zn070515/BountyGrid/internal/domain"
)

func TestBuildBinding(t *testing.T) {
	b := buildBinding("draft/f1/1", "ap1", "content-hash")
	if b.RequestID != "submit/draft/f1/1/ap1" {
		t.Fatalf("request id = %q", b.RequestID)
	}
	if b.Parameters["report_id"] != "draft/f1/1" || b.Parameters["report_content_hash"] != "content-hash" {
		t.Fatalf("parameters = %v", b.Parameters)
	}
	raw, _ := json.Marshal(b.Parameters)
	if b.ParametersHash != domain.CanonicalParametersHash(raw) {
		t.Fatalf("hash = %q", b.ParametersHash)
	}
}

func TestApprovalFileToDomain(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	asset := domain.AssetID("a1")
	a := approvalFile{
		ID: "ap1", ApprovedBy: "human", ApprovedAt: now,
		RequestID: "submit/draft/f1/1/ap1", Action: domain.ActionReportSubmit,
		ProgramID: "p1", AssetID: &asset, ResourceID: "draft/f1/1",
		ParametersHash: "hash", ExpiresAt: now.Add(time.Hour), SingleUse: true,
	}
	got := a.toDomain()
	if got.ID != "ap1" || got.Action != domain.ActionReportSubmit ||
		got.AssetID == nil || *got.AssetID != "a1" || !got.SingleUse {
		t.Fatalf("approval = %+v", got)
	}
}

func TestParseSubmitOptions(t *testing.T) {
	opts, err := parseSubmitOptions([]string{
		"-report", "draft/f1/1", "-account", "vulbox/account", "-approval", "a.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ReportID != "draft/f1/1" || opts.AccountID != "vulbox/account" || opts.Approval != "a.json" {
		t.Fatalf("opts = %+v", opts)
	}

	opts, err = parseSubmitOptions([]string{
		"-print-binding", "-report", "draft/f1/1", "-approval-id", "ap1",
	})
	if err != nil || !opts.PrintBinding {
		t.Fatalf("binding opts = %+v err=%v", opts, err)
	}

	if _, err := parseSubmitOptions([]string{"-report", "draft/f1/1"}); err == nil {
		t.Fatal("missing account/approval should fail")
	}
	if _, err := parseSubmitOptions([]string{"-print-binding"}); err == nil {
		t.Fatal("binding without report/approval-id should fail")
	}
}
