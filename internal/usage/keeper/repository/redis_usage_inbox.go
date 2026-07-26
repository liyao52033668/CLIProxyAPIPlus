package repository

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"gorm.io/gorm"
)

const (
	RedisUsageInboxStatusPending       = "pending"
	RedisUsageInboxStatusProcessed     = "processed"
	RedisUsageInboxStatusDecodeFailed  = "decode_failed"
	RedisUsageInboxStatusProcessFailed = "process_failed"
	RedisUsageInboxStatusDiscarded     = "discarded"

	redisUsageInboxMaxErrorLength     = 1024
	redisUsageInboxMaxProcessAttempts = 5
)

func InsertRedisUsageInboxMessages(db *gorm.DB, inputs []dto.RedisInboxInsert) ([]entities.RedisUsageInbox, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	rows := make([]entities.RedisUsageInbox, 0, len(inputs))
	// Convert raw Redis messages into inbox rows first; later persistence only handles the normalized model.
	for _, input := range inputs {
		hash := sha256.Sum256([]byte(input.RawMessage))
		rows = append(rows, entities.RedisUsageInbox{
			QueueKey:     strings.TrimSpace(input.QueueKey),
			MessageHash:  fmt.Sprintf("%x", hash),
			RawMessage:   input.RawMessage,
			Status:       RedisUsageInboxStatusPending,
			AttemptCount: 0,
			PoppedAt:     input.PoppedAt.UTC(),
		})
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		// Redis pull batch size remains config-driven; only split DB writes into safe sizes here.
		return tx.CreateInBatches(&rows, insertBatchSize(entities.RedisUsageInbox{})).Error
	}); err != nil {
		return nil, err
	}
	return rows, nil
}

func MarkRedisUsageInboxProcessed(db *gorm.DB, id uint, eventKey string, processedAt time.Time) error {
	return db.Model(&entities.RedisUsageInbox{}).Where("id = ?", id).Updates(map[string]any{
		"status":          RedisUsageInboxStatusProcessed,
		"usage_event_key": eventKey,
		"processed_at":    processedAt.UTC(),
		"last_error":      "",
	}).Error
}

func MarkRedisUsageInboxDecodeFailed(db *gorm.DB, id uint, decodeErr error) error {
	return markRedisUsageInboxFailed(db, id, RedisUsageInboxStatusDecodeFailed, decodeErr)
}

func MarkRedisUsageInboxProcessFailed(db *gorm.DB, id uint, processErr error) error {
	return db.Model(&entities.RedisUsageInbox{}).Where("id = ?", id).Updates(map[string]any{
		"status": gorm.Expr(
			"CASE WHEN attempt_count + ? >= ? THEN ? ELSE ? END",
			1,
			redisUsageInboxMaxProcessAttempts,
			RedisUsageInboxStatusDiscarded,
			RedisUsageInboxStatusProcessFailed,
		),
		"attempt_count": gorm.Expr("attempt_count + ?", 1),
		"last_error":    boundedRedisUsageInboxError(processErr),
	}).Error
}

// ListProcessableRedisUsageInbox returns pending and retryable rows, excluding decode-failed or discarded ones.
func ListProcessableRedisUsageInbox(db *gorm.DB, limit int) ([]entities.RedisUsageInbox, error) {
	query := db.Where("status = ? OR status = ?", RedisUsageInboxStatusPending, RedisUsageInboxStatusProcessFailed).Order("id asc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []entities.RedisUsageInbox
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func ListPendingRedisUsageInbox(db *gorm.DB, limit int) ([]entities.RedisUsageInbox, error) {
	query := db.Where("status = ?", RedisUsageInboxStatusPending).Order("id asc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []entities.RedisUsageInbox
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CleanupRedisUsageInbox removes completed and failed Redis inbox raw messages; pending rows are never deleted here.
// processed rows are kept until the next local day starts; decode_failed/process_failed/discarded are retained 7 days for troubleshooting.
func CleanupRedisUsageInbox(db *gorm.DB, now time.Time) (dto.RedisUsageInboxCleanupResult, error) {
	localNow := now.In(time.Local)
	localDayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)
	processedCutoff := localDayStart.UTC()
	failedCutoff := now.UTC().AddDate(0, 0, -7)
	result := dto.RedisUsageInboxCleanupResult{}

	processedDelete := db.Where("status = ? AND processed_at IS NOT NULL AND processed_at < ?", RedisUsageInboxStatusProcessed, processedCutoff).Delete(&entities.RedisUsageInbox{})
	if processedDelete.Error != nil {
		return result, processedDelete.Error
	}
	result.ProcessedDeleted = processedDelete.RowsAffected

	failedDelete := db.Where("status IN ? AND updated_at < ?", []string{RedisUsageInboxStatusDecodeFailed, RedisUsageInboxStatusProcessFailed, RedisUsageInboxStatusDiscarded}, failedCutoff).Delete(&entities.RedisUsageInbox{})
	if failedDelete.Error != nil {
		return result, failedDelete.Error
	}
	result.FailedDeleted = failedDelete.RowsAffected

	return result, nil
}

func markRedisUsageInboxFailed(db *gorm.DB, id uint, status string, err error) error {
	return db.Model(&entities.RedisUsageInbox{}).Where("id = ?", id).Updates(map[string]any{
		"status":        status,
		"attempt_count": gorm.Expr("attempt_count + ?", 1),
		"last_error":    boundedRedisUsageInboxError(err),
	}).Error
}

func boundedRedisUsageInboxError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) <= redisUsageInboxMaxErrorLength {
		return message
	}
	message = message[:redisUsageInboxMaxErrorLength]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
