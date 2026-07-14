//go:build !cgo

package backup

import (
	"context"
	"database/sql"
	"fmt"
)

func copySQLiteDatabase(context.Context, *sql.DB, string) error {
	return fmt.Errorf("sqlite database backup requires cgo")
}
