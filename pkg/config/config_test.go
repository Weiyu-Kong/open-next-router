package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "onr.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLocalBillingDefaults(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, "redis:\n  enabled: true\n  access_key_hash_secret: test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Billing.Currency != "CNY" || cfg.Billing.InitialCredit != "0" {
		t.Fatalf("billing defaults=%+v", cfg.Billing)
	}
	if cfg.Redis.BillingStream != "onr:billing-events" {
		t.Fatalf("billing stream=%q", cfg.Redis.BillingStream)
	}
}

func TestLoadAdminWebToken(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, "admin:\n  web:\n    token: fixed-admin-token\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Web.Token != "fixed-admin-token" {
		t.Fatalf("admin web token=%q", cfg.Admin.Web.Token)
	}
}

func TestLocalBillingRequiresRedis(t *testing.T) {
	_, err := Load(writeConfigFile(t, "billing:\n  enabled: true\n  currency: CNY\n  initial_credit: '10'\n"))
	if err == nil || !strings.Contains(err.Error(), "billing.enabled=true requires redis.enabled=true") {
		t.Fatalf("err=%v", err)
	}
}

func TestLocalBillingRejectsInvalidInitialCredit(t *testing.T) {
	_, err := Load(writeConfigFile(t, "billing:\n  enabled: true\n  currency: CNY\n  initial_credit: '-1'\nredis:\n  enabled: true\n  access_key_hash_secret: test\n"))
	if err == nil || !strings.Contains(err.Error(), "billing.initial_credit") {
		t.Fatalf("err=%v", err)
	}
}
