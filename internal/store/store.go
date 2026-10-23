package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Rewind2026/wgpanel/internal/config"
)

// readPoolSize 是只读连接数。WAL 模式下读不阻塞写、读之间也不互斥，
// 单连接池会让 SSE、REST 与调谐器排队，从而把 P99 延迟拉到采集间隔量级。
const readPoolSize = 4

// Store 是持久化层门面。
type Store struct {
	// db 处理全部读写，写操作靠 busy_timeout 与短事务串行化。
	db *sql.DB
	// ro 是只读副本连接池，供高频查询使用。
	ro *sql.DB
}

// Open 打开数据库并应用 PRAGMA 与迁移。
func Open(ctx context.Context, cfg config.Config) (*Store, error) {
	dsn := dsn(cfg.DBPath())

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	// modernc/sqlite 单写者模型：限制写连接数可避免写锁竞争导致的 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	ro, err := sql.Open("sqlite", dsn)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open sqlite read pool: %w", err)
	}
	ro.SetMaxOpenConns(readPoolSize)
	ro.SetMaxIdleConns(readPoolSize)
	ro.SetConnMaxLifetime(0)

	s := &Store{db: db, ro: ro}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// dsn 组装带 PRAGMA 的 SQLite 连接串。
func dsn(path string) string {
	return fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
}

// NewWithDB 便于测试注入自定义连接。
func NewWithDB(db *sql.DB) *Store { return &Store{db: db, ro: db} }

// OpenInMemory 打开一份仅存在于内存的数据库并应用全部迁移。
//
// 供其它包的测试使用：策略、备份、服务层的测试都需要真实的 schema 与约束
// （外键、唯一索引），手写建表语句既冗余又会在迁移演进后失真。
// 每份库使用独立的名字，避免同进程内多个 Store 共享同一份数据。
func OpenInMemory() (*Store, error) {
	dsn := fmt.Sprintf("file:mem-%d-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		os.Getpid(), atomic.AddUint64(&memDBCounter, 1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open in-memory sqlite: %w", err)
	}
	// modernc/sqlite 单写者模型；测试并发写时共享一份连接反而更接近真实行为。
	db.SetMaxOpenConns(1)
	s := NewWithDB(db)
	if err := s.migrate(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// memDBCounter 为每个内存库生成唯一名字。
var memDBCounter uint64

// DB 暴露底层连接供健康检查使用。
func (s *Store) DB() *sql.DB { return s.db }

// RO 返回只读连接池。
func (s *Store) RO() *sql.DB { return s.ro }

// Close 关闭数据库连接。
// NewWithDB 让读写共用同一个 *sql.DB，这种情况下只关一次——
// 重复 Close 虽然不报错，但「谁负责关」会变得没有依据，
// 出问题时也无法判断是初始化方还是使用方关的。
func (s *Store) Close() error {
	if s.db == s.ro {
		return s.db.Close()
	}
	err := s.db.Close()
	if roErr := s.ro.Close(); err == nil {
		err = roErr
	}
	return err
}

// WithTx 在事务中执行 fn。
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Health 探测数据库连通性。
func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

// HasEncryptedSecrets 判断库里是否已经存在需要主密钥才能解开的密文。
//
// 用于启动前的兜底校验：数据库里已经有服务端私钥，主密钥却不见了，
// 说明数据目录被拆散了。此时必须让进程起不来，而不是生成一把新密钥
// 把所有私钥变成永久打不开的密文。
func (s *Store) HasEncryptedSecrets(ctx context.Context) (bool, error) {
	var n int
	err := s.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM server_config WHERE private_key <> ''`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: probe encrypted secrets: %w", err)
	}
	return n > 0, nil
}

func nowUnix() int64 { return time.Now().Unix() }
