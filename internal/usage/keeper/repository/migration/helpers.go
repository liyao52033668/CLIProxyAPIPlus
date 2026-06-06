package migration

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const redisUsageInboxStatusProcessed = "processed"

type usageIdentityStatsDelta struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	FirstUsedAt     *time.Time
	LastUsedAt      *time.Time
	MaxUsageEventID uint
}

func dropColumnIfExists(tx *gorm.DB, tableName string, columnName string) error {
	if !tx.Migrator().HasTable(tableName) || !tx.Migrator().HasColumn(tableName, columnName) {
		return nil
	}
	statement := &gorm.Statement{DB: tx}
	statement.WriteString("ALTER TABLE ")
	statement.WriteQuoted(clause.Table{Name: tableName})
	statement.WriteString(" DROP COLUMN ")
	statement.WriteQuoted(clause.Column{Name: columnName})
	if err := tx.Exec(statement.SQL.String()).Error; err != nil {
		return fmt.Errorf("drop %s.%s column: %w", tableName, columnName, err)
	}
	return nil
}

func dropIndexIfExists(tx *gorm.DB, tableName string, indexName string) error {
	if !tx.Migrator().HasTable(tableName) || !tx.Migrator().HasIndex(tableName, indexName) {
		return nil
	}
	statement := &gorm.Statement{DB: tx}
	statement.WriteString("DROP INDEX IF EXISTS ")
	statement.WriteQuoted(clause.Column{Name: indexName})
	if err := tx.Exec(statement.SQL.String()).Error; err != nil {
		return fmt.Errorf("drop %s index %s: %w", tableName, indexName, err)
	}
	return nil
}

func legacyTableHasDeletedAt(tx *gorm.DB, table string) bool {
	return tx.Migrator().HasColumn(table, "deleted_at")
}
