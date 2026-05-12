package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/entities"
	"gorm.io/gorm"
)

func removePrefixUsageIdentitiesMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&entities.UsageIdentity{}) {
		return nil
	}
	if err := tx.Exec(`
		DELETE FROM usage_identities
		WHERE auth_type = ?
			AND LOWER(TRIM(identity)) IN ('gemini', 'claude', 'codex', 'vertex', 'openai')`, entities.UsageIdentityAuthTypeAIProvider).Error; err != nil {
		return fmt.Errorf("remove prefix-generated usage identities: %w", err)
	}
	return nil
}
