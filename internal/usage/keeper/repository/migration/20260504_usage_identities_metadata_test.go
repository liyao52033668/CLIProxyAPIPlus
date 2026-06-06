package migration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
)

func TestOpenDatabaseUsageIdentityMigratesLegacyMetadataAndDropsOldTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-identities.db")
	seedLegacyUsageIdentityTables(t, dbPath)

	db := openMigratedDatabase(t, dbPath)
	defer closeOpenedDatabase(t, db)

	if !db.Migrator().HasTable(&entities.UsageIdentity{}) {
		t.Fatal("expected usage_identities table to exist")
	}
	if db.Migrator().HasTable("auth_files") {
		t.Fatal("expected auth_files table to be dropped")
	}
	if db.Migrator().HasTable("provider_metadata") {
		t.Fatal("expected provider_metadata table to be dropped")
	}

	var identities []entities.UsageIdentity
	if err := db.Order("auth_type asc, identity asc").Find(&identities).Error; err != nil {
		t.Fatalf("load usage identities: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("expected unmatched AI provider raw identities to be deleted, got %d: %+v", len(identities), identities)
	}

	oauth := findUsageIdentity(t, identities, entities.UsageIdentityAuthTypeAuthFile, "auth-1")
	if oauth.Name != "person@example.com" || oauth.AuthTypeName != "oauth" || oauth.Type != "claude" || oauth.Provider != "claude" {
		t.Fatalf("unexpected oauth identity mapping: %+v", oauth)
	}
	if oauth.TotalRequests != 3 || oauth.SuccessCount != 2 || oauth.FailureCount != 1 || oauth.InputTokens != 31 || oauth.OutputTokens != 41 || oauth.ReasoningTokens != 11 || oauth.CachedTokens != 7 || oauth.TotalTokens != 90 {
		t.Fatalf("unexpected oauth identity stats: %+v", oauth)
	}
	if oauth.FirstUsedAt == nil || !oauth.FirstUsedAt.Equal(time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected oauth first used timestamp: %+v", oauth.FirstUsedAt)
	}
	if oauth.LastUsedAt == nil || !oauth.LastUsedAt.Equal(time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected oauth last used timestamp: %+v", oauth.LastUsedAt)
	}
	if oauth.StatsUpdatedAt == nil {
		t.Fatal("expected oauth stats_updated_at to be set")
	}
	if oauth.LastAggregatedUsageEventID != 3 {
		t.Fatalf("expected oauth last aggregated usage event id 3, got %d", oauth.LastAggregatedUsageEventID)
	}

	if countUsageIdentities(t, db, entities.UsageIdentityAuthTypeAIProvider, "api-source-1") != 0 {
		t.Fatal("expected unmatched active provider raw identity to be deleted")
	}

	deletedOAuth := findUsageIdentity(t, identities, entities.UsageIdentityAuthTypeAuthFile, "auth-deleted")
	if !deletedOAuth.IsDeleted || deletedOAuth.DeletedAt == nil || !deletedOAuth.DeletedAt.Equal(time.Date(2026, 5, 4, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected deleted auth file state to be preserved, got %+v", deletedOAuth)
	}
	if deletedOAuth.TotalRequests != 1 || deletedOAuth.TotalTokens != 100 || deletedOAuth.LastAggregatedUsageEventID != 6 {
		t.Fatalf("expected deleted auth file usage stats to be backfilled, got %+v", deletedOAuth)
	}

	if countUsageIdentities(t, db, entities.UsageIdentityAuthTypeAIProvider, "api-deleted") != 0 {
		t.Fatal("expected unmatched deleted provider raw identity to be deleted")
	}
}

func TestOpenDatabaseSkipsUsageIdentityMetadataMigrationWhenLegacyTablesAreMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "no-legacy-identities.db")
	seedPerformanceIndexMigrationDatabase(t, dbPath)

	db := openMigratedDatabase(t, dbPath)
	defer closeOpenedDatabase(t, db)

	if !db.Migrator().HasTable(&entities.UsageIdentity{}) {
		t.Fatal("expected usage_identities table to exist")
	}
	var count int64
	if err := db.Model(&entities.UsageIdentity{}).Count(&count).Error; err != nil {
		t.Fatalf("count usage identities: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no usage identities without legacy metadata, got %d", count)
	}
	if db.Migrator().HasTable("auth_files") || db.Migrator().HasTable("provider_metadata") {
		t.Fatal("expected legacy metadata tables not to be recreated")
	}
}

func TestUsageIdentityMetadataInsertSQLUsesFixedStatements(t *testing.T) {
	testCases := []struct {
		name             string
		got              string
		wantTable        string
		wantDeletedExpr  string
		wantDeletedValue string
	}{
		{
			name:             "auth files with deleted_at",
			got:              authFilesUsageIdentityInsertSQL(true),
			wantTable:        "FROM auth_files",
			wantDeletedExpr:  "deleted_at IS NOT NULL",
			wantDeletedValue: ", deleted_at",
		},
		{
			name:             "auth files without deleted_at",
			got:              authFilesUsageIdentityInsertSQL(false),
			wantTable:        "FROM auth_files",
			wantDeletedExpr:  ", false,",
			wantDeletedValue: ", NULL",
		},
		{
			name:             "provider metadata with deleted_at",
			got:              providerMetadataUsageIdentityInsertSQL(true),
			wantTable:        "FROM provider_metadata",
			wantDeletedExpr:  "deleted_at IS NOT NULL",
			wantDeletedValue: ", deleted_at",
		},
		{
			name:             "provider metadata without deleted_at",
			got:              providerMetadataUsageIdentityInsertSQL(false),
			wantTable:        "FROM provider_metadata",
			wantDeletedExpr:  ", false,",
			wantDeletedValue: ", NULL",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if !strings.Contains(testCase.got, testCase.wantTable) {
				t.Fatalf("SQL %q does not contain table marker %q", testCase.got, testCase.wantTable)
			}
			if !strings.Contains(testCase.got, testCase.wantDeletedExpr) {
				t.Fatalf("SQL %q does not contain deleted expression %q", testCase.got, testCase.wantDeletedExpr)
			}
			if !strings.Contains(testCase.got, testCase.wantDeletedValue) {
				t.Fatalf("SQL %q does not contain deleted value %q", testCase.got, testCase.wantDeletedValue)
			}
		})
	}
}
