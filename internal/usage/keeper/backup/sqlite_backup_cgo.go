//go:build cgo

package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mattn/go-sqlite3"
)

func copySQLiteDatabase(ctx context.Context, sourceDB *sql.DB, destPath string) error {
	sourceConn, err := sourceDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open source database connection: %w", err)
	}
	defer sourceConn.Close()

	destDB, err := sql.Open("sqlite3", destPath)
	if err != nil {
		return fmt.Errorf("open backup database: %w", err)
	}
	defer destDB.Close()
	destConn, err := destDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open backup database connection: %w", err)
	}
	defer destConn.Close()

	return sourceConn.Raw(func(sourceDriverConn any) error {
		sourceSQLite, ok := sourceDriverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("source database connection is not sqlite3")
		}
		return destConn.Raw(func(destDriverConn any) error {
			destSQLite, ok := destDriverConn.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("backup database connection is not sqlite3")
			}
			backup, err := destSQLite.Backup("main", sourceSQLite, "main")
			if err != nil {
				return fmt.Errorf("start sqlite backup: %w", err)
			}
			var backupErr error
			for {
				if err := ctx.Err(); err != nil {
					backupErr = err
					break
				}
				done, err := backup.Step(100)
				if err != nil {
					backupErr = fmt.Errorf("copy sqlite backup: %w", err)
					break
				}
				if done {
					break
				}
			}
			if err := backup.Close(); err != nil {
				backupErr = errors.Join(backupErr, fmt.Errorf("close sqlite backup: %w", err))
			}
			return backupErr
		})
	})
}
