package capability

import (
	"context"
	"errors"
)

// NetworkUsageStore is the durable counter seam consumed by
// PostgresDailyQuota. The PostgreSQL Repo implements ConsumeNetworkUsage.
type NetworkUsageStore interface {
	ConsumeNetworkUsage(ctx context.Context, accountID, programID, interfaceName, businessDate string, limit int64) (bool, error)
}

// PostgresDailyQuota adapts a durable network_usage store to DailyQuota.
// Inject it as:
//
//	broker := capability.NewMemoryBroker(
//	    capability.WithDailyQuota(capability.PostgresDailyQuota{Store: repo}),
//	)
//	h.SetBroker(broker)
//
// The broker (RPS/concurrency) remains process-local; only the daily counter
// survives a restart.
type PostgresDailyQuota struct {
	Store NetworkUsageStore
}

func (q PostgresDailyQuota) Consume(ctx context.Context, key, businessDate string, limit int64) (bool, error) {
	if q.Store == nil {
		return false, errors.New("capability: postgres daily quota has no store")
	}
	account, program, iface, err := SplitQuotaKey(key)
	if err != nil {
		return false, err
	}
	return q.Store.ConsumeNetworkUsage(ctx, account, program, iface, businessDate, limit)
}
