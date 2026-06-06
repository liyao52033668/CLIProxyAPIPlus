package migration

import (
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"gorm.io/gorm"
)

func migrateUsageIdentitiesMetadataMigration(tx *gorm.DB) error {
	now := time.Now().UTC()
	if tx.Migrator().HasTable("auth_files") {
		if err := tx.Exec(authFilesUsageIdentityInsertSQL(legacyTableHasDeletedAt(tx, "auth_files")), entities.UsageIdentityAuthTypeAuthFile, "oauth", now, now).Error; err != nil {
			return fmt.Errorf("migrate auth_files to usage_identities: %w", err)
		}
	}
	if tx.Migrator().HasTable("provider_metadata") {
		if err := tx.Exec(providerMetadataUsageIdentityInsertSQL(legacyTableHasDeletedAt(tx, "provider_metadata")), entities.UsageIdentityAuthTypeAIProvider, "apikey", now, now).Error; err != nil {
			return fmt.Errorf("migrate provider_metadata to usage_identities: %w", err)
		}
	}
	return nil
}

func authFilesUsageIdentityInsertSQL(hasDeletedAt bool) string {
	if hasDeletedAt {
		return `
			INSERT INTO usage_identities (name, auth_type, auth_type_name, identity, type, provider, is_deleted, created_at, updated_at, deleted_at)
			SELECT COALESCE(NULLIF(TRIM(email), ''), NULLIF(TRIM(label), ''), NULLIF(TRIM(name), ''), auth_index),
				?, ?, auth_index, type, provider, deleted_at IS NOT NULL, COALESCE(created_at, ?), ?, deleted_at
			FROM auth_files
			WHERE auth_index IS NOT NULL AND TRIM(auth_index) != ''
			ON CONFLICT(auth_type, identity) DO UPDATE SET
				name = excluded.name,
				auth_type_name = excluded.auth_type_name,
				type = excluded.type,
				provider = excluded.provider,
				is_deleted = excluded.is_deleted,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at`
	}
	return `
			INSERT INTO usage_identities (name, auth_type, auth_type_name, identity, type, provider, is_deleted, created_at, updated_at, deleted_at)
			SELECT COALESCE(NULLIF(TRIM(email), ''), NULLIF(TRIM(label), ''), NULLIF(TRIM(name), ''), auth_index),
				?, ?, auth_index, type, provider, false, COALESCE(created_at, ?), ?, NULL
			FROM auth_files
			WHERE auth_index IS NOT NULL AND TRIM(auth_index) != ''
			ON CONFLICT(auth_type, identity) DO UPDATE SET
				name = excluded.name,
				auth_type_name = excluded.auth_type_name,
				type = excluded.type,
				provider = excluded.provider,
				is_deleted = excluded.is_deleted,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at`
}

func providerMetadataUsageIdentityInsertSQL(hasDeletedAt bool) string {
	if hasDeletedAt {
		return `
			INSERT INTO usage_identities (name, auth_type, auth_type_name, identity, type, provider, is_deleted, created_at, updated_at, deleted_at)
			SELECT display_name, ?, ?, lookup_key, provider_type, display_name, deleted_at IS NOT NULL, COALESCE(created_at, ?), ?, deleted_at
			FROM provider_metadata
			WHERE lookup_key IS NOT NULL AND TRIM(lookup_key) != ''
			ON CONFLICT(auth_type, identity) DO UPDATE SET
				name = excluded.name,
				auth_type_name = excluded.auth_type_name,
				type = excluded.type,
				provider = excluded.provider,
				is_deleted = excluded.is_deleted,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at`
	}
	return `
			INSERT INTO usage_identities (name, auth_type, auth_type_name, identity, type, provider, is_deleted, created_at, updated_at, deleted_at)
			SELECT display_name, ?, ?, lookup_key, provider_type, display_name, false, COALESCE(created_at, ?), ?, NULL
			FROM provider_metadata
			WHERE lookup_key IS NOT NULL AND TRIM(lookup_key) != ''
			ON CONFLICT(auth_type, identity) DO UPDATE SET
				name = excluded.name,
				auth_type_name = excluded.auth_type_name,
				type = excluded.type,
				provider = excluded.provider,
				is_deleted = excluded.is_deleted,
				deleted_at = excluded.deleted_at,
				updated_at = excluded.updated_at`
}
