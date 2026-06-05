package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func main() {

	src := "./db/usage.db"
	dst := "./db/usage_recovered.db"

	_ = os.Remove(dst)

	db, err := sql.Open("sqlite3", src)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	newDB, err := sql.Open("sqlite3", dst)
	if err != nil {
		log.Fatal(err)
	}
	defer newDB.Close()

	fmt.Println("读取表列表...")

	tables, err := db.Query(`
		SELECT name, sql
		FROM sqlite_master
		WHERE type='table'
		AND name NOT LIKE 'sqlite_%'
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer tables.Close()

	type Table struct {
		Name string
		SQL  string
	}

	var allTables []Table

	for tables.Next() {
		var t Table
		if err := tables.Scan(&t.Name, &t.SQL); err != nil {
			continue
		}
		allTables = append(allTables, t)
	}

	fmt.Printf("发现 %d 张表\n\n", len(allTables))

	for _, tbl := range allTables {

		fmt.Printf("恢复表: %s\n", tbl.Name)

		if tbl.SQL != "" {
			_, err := newDB.Exec(tbl.SQL)
			if err != nil {
				fmt.Printf("创建表失败: %v\n", err)
				continue
			}
		}

		rows, err := db.Query("SELECT * FROM `" + tbl.Name + "`")
		if err != nil {
			fmt.Printf("读取失败: %v\n\n", err)
			continue
		}

		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			continue
		}

		placeholders := make([]string, len(cols))
		for i := range placeholders {
			placeholders[i] = "?"
		}

		insertSQL := fmt.Sprintf(
			"INSERT INTO `%s` VALUES (%s)",
			tbl.Name,
			strings.Join(placeholders, ","),
		)

		tx, _ := newDB.Begin()

		insertStmt, err := tx.Prepare(insertSQL)
		if err != nil {
			rows.Close()
			tx.Rollback()
			continue
		}

		count := 0

		for rows.Next() {

			values := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))

			for i := range values {
				ptrs[i] = &values[i]
			}

			err := rows.Scan(ptrs...)
			if err != nil {
				continue
			}

			_, err = insertStmt.Exec(values...)
			if err != nil {
				continue
			}

			count++
		}

		insertStmt.Close()
		tx.Commit()
		rows.Close()

		fmt.Printf("成功恢复 %d 行\n\n", count)
	}

	fmt.Println("===================================")
	fmt.Println("恢复完成，开始 integrity_check ...")

	// integrity_check
	rows, err := newDB.Query("PRAGMA integrity_check;")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	var result string
	for rows.Next() {
		rows.Scan(&result)
		fmt.Println("PRAGMA integrity_check:", result)
	}

	if result == "ok" {
		fmt.Println("✅ 数据库完整，可直接替换原 usage.db 使用")
	} else {
		fmt.Println("⚠️ 数据库存在问题，请谨慎替换")
	}

	fmt.Println("新数据库文件:", dst)
	fmt.Println("===================================")
}