package postgres

import (
	"context"
	"testing"
	"time"
)

func TestConsumeNetworkUsage(t *testing.T) {
	r, ctx := integrationRepo(t)
	suffix := time.Now().UTC().Format("150405.000000000")
	account := "it-usage-acc-" + suffix
	program := "it-usage-prog-" + suffix

	t.Cleanup(func() {
		_, _ = r.Pool.Exec(context.Background(),
			`DELETE FROM network_usage WHERE account_id=$1`, account)
	})

	const iface = "http"
	const day = "2026-11-01"

	// Limit 2 permits exactly two requests.
	for i := 1; i <= 2; i++ {
		ok, err := r.ConsumeNetworkUsage(ctx, account, program, iface, day, 2)
		if err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("consume %d = false, want true", i)
		}
	}
	ok, err := r.ConsumeNetworkUsage(ctx, account, program, iface, day, 2)
	if err != nil {
		t.Fatalf("over-limit consume: %v", err)
	}
	if ok {
		t.Fatal("third consume = true, want false at the limit")
	}

	// The rejected request must not have incremented the counter.
	var count int64
	if err := r.Pool.QueryRow(ctx,
		`SELECT count FROM network_usage WHERE account_id=$1 AND program_id=$2 AND interface=$3 AND business_date=$4::date`,
		account, program, iface, day,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("stored count = %d, want 2", count)
	}

	// A different business date has its own counter.
	nextDay := "2026-11-02"
	ok, err = r.ConsumeNetworkUsage(ctx, account, program, iface, nextDay, 1)
	if err != nil || !ok {
		t.Fatalf("next-day consume = %v, %v", ok, err)
	}

	// A different program has its own counter.
	ok, err = r.ConsumeNetworkUsage(ctx, account, program+"-other", iface, day, 1)
	if err != nil || !ok {
		t.Fatalf("other-program consume = %v, %v", ok, err)
	}

	// Limit 0 has no ceiling but keeps counting: two requests are recorded, and
	// a later limit of 2 is already exhausted.
	const noCap = "http-nocap"
	for i := 0; i < 2; i++ {
		ok, err = r.ConsumeNetworkUsage(ctx, account, program, noCap, day, 0)
		if err != nil || !ok {
			t.Fatalf("uncapped consume %d = %v, %v", i, ok, err)
		}
	}
	ok, err = r.ConsumeNetworkUsage(ctx, account, program, noCap, day, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("capped consume after two uncapped requests = true, want false")
	}
	if err := r.Pool.QueryRow(ctx,
		`SELECT count FROM network_usage WHERE account_id=$1 AND program_id=$2 AND interface=$3 AND business_date=$4::date`,
		account, program, noCap, day,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("uncapped stored count = %d, want 2", count)
	}
}
