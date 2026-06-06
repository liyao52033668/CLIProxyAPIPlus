package store

import (
	"context"
	"strings"
	"testing"
)

func TestNewPostgresStore_RejectsInvalidIdentifiersBeforeConnecting(t *testing.T) {
	testCases := []struct {
		name        string
		config      PostgresStoreConfig
		wantMessage string
	}{
		{
			name: "schema",
			config: PostgresStoreConfig{
				Schema:      "public;drop schema public",
				ConfigTable: defaultConfigTable,
				AuthTable:   defaultAuthTable,
				UsageTable:  defaultUsageTable,
			},
			wantMessage: "invalid schema identifier",
		},
		{
			name: "config table",
			config: PostgresStoreConfig{
				ConfigTable: "config-store",
				AuthTable:   defaultAuthTable,
				UsageTable:  defaultUsageTable,
			},
			wantMessage: "invalid config table identifier",
		},
		{
			name: "auth table",
			config: PostgresStoreConfig{
				ConfigTable: defaultConfigTable,
				AuthTable:   "auth store",
				UsageTable:  defaultUsageTable,
			},
			wantMessage: "invalid auth table identifier",
		},
		{
			name: "usage table",
			config: PostgresStoreConfig{
				ConfigTable: defaultConfigTable,
				AuthTable:   defaultAuthTable,
				UsageTable:  "usage.store",
			},
			wantMessage: "invalid usage table identifier",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			config := testCase.config
			config.DSN = "postgres://user:pass@127.0.0.1:1/testdb"
			config.SpoolDir = t.TempDir()

			_, err := NewPostgresStore(context.Background(), config)
			if err == nil {
				t.Fatalf("NewPostgresStore succeeded for invalid %s", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("expected error containing %q, got %v", testCase.wantMessage, err)
			}
		})
	}
}
