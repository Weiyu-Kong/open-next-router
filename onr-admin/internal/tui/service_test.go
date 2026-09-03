package tui

import (
	"context"
	"testing"

	"github.com/r9s-ai/open-next-router/pkg/config"
)

func TestAdminServiceSnapshotWithoutRedis(t *testing.T) {
	service := &adminService{cfg: &config.Config{}}
	snapshot := service.Snapshot(context.Background())
	if snapshot.RedisEnabled || snapshot.RedisReachable || snapshot.BillingEnabled {
		t.Fatalf("unexpected disabled snapshot: %+v", snapshot)
	}
}
