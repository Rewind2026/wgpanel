package store

import (
	"database/sql"
	"fmt"
)

// sqlOpenMemory 打开一个共享缓存的内存 SQLite，便于测试隔离。
func sqlOpenMemory() (*sql.DB, error) {
	dsn := "file::memory:?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open memory sqlite: %w", err)
	}
	// 每个测试独占一份缓存库，避免跨测试串数据。
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
