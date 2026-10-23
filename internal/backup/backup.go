// Package backup 实现加密备份与一键恢复。
//
// 备份内容：配置与身份的逻辑快照，加密后落盘为单个 .wgbak 文件。
//
// 为什么不用「直接复制 .db 文件」：WAL 模式下只复制主库文件会丢掉 -wal 里尚未
// checkpoint 的事务，恢复后数据不一致。这里在写事务串行的前提下导出逻辑数据，
// 天然得到一致快照。
//
// 为什么用「列名 + 按列有序的值」而不是「领域对象 JSON」：领域模型带 omitempty 与
// json:"-"（私钥、密码哈希），JSON 往返会丢掉它们——恢复后所有用户密码失效、
// 所有客户端私钥为空。列有序的转储不含这类语义，值原样进出。
//
// 密钥：使用面板主密钥。备份文件泄漏到别处也无法解开；反之更换主密钥后
// 旧备份不可恢复，这一点在文档与接口提示中都要写明。
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// FormatVersion 是备份格式版本。恢复时版本不匹配直接拒绝，避免误解密出错误结构。
const FormatVersion = 1

// Service 负责备份与恢复。
type Service struct {
	store    *store.Store
	backups  *store.BackupRepo
	settings *store.SettingRepo
	cipher   *authn.Cipher
	dataDir  string
	log      *slog.Logger
}

// New 构造备份服务。dataDir 是面板数据目录，备份写在它下面。
func New(st *store.Store, cipher *authn.Cipher, dataDir string, log *slog.Logger) *Service {
	return &Service{
		store:    st,
		backups:  st.BackupRepo(),
		settings: st.SettingRepo(),
		cipher:   cipher,
		dataDir:  dataDir,
		log:      log,
	}
}

// TableDump 是单表的转储结果。
type TableDump struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Payload 是备份的明文内容。
type Payload struct {
	FormatVersion int         `json:"formatVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
	SchemaVersion int         `json:"schemaVersion"`
	Tables        []TableDump `json:"tables"`
}

// TableNames 是参与备份的表，顺序即恢复顺序。
//
// server_config 只保留一行，peer / user 是完整列表。
// 刻意不备份 schema_migration：它描述的是运行结构而非数据，
// 恢复一份旧版本号会让后续迁移被跳过。
var TableNames = []string{"server_config", "peer", "user", "app_setting"}

// envelope 是落盘文件的结构。
type envelope struct {
	FormatVersion int    `json:"formatVersion"`
	CreatedAt     int64  `json:"createdAt"`
	Cipher        string `json:"cipher"`
	SHA256        string `json:"sha256"`
	Payload       string `json:"payload"`
}

// Create 生成一次备份，返回索引记录。kind 取 manual / scheduled。
func (s *Service) Create(ctx context.Context, kind string, operatorID *int64) (*domain.BackupRecord, error) {
	payload, err := s.export(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("backup: marshal payload: %w", err)
	}
	sealed, err := s.cipher.Encrypt(string(raw))
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(sealed))
	now := time.Now().UTC()
	body, err := json.Marshal(envelope{
		FormatVersion: FormatVersion,
		CreatedAt:     now.Unix(),
		Cipher:        "aes-256-gcm",
		SHA256:        hex.EncodeToString(sum[:]),
		Payload:       sealed,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: marshal envelope: %w", err)
	}

	dir, err := s.backupDir(ctx)
	if err != nil {
		return nil, err
	}
	filename := uniqueFilename(dir, now, kind)
	if err := os.WriteFile(filepath.Join(dir, filename), body, 0o600); err != nil {
		return nil, fmt.Errorf("backup: write file: %w", err)
	}

	rec := &domain.BackupRecord{
		Filename:      filename,
		SizeBytes:     int64(len(body)),
		SHA256:        hex.EncodeToString(sum[:]),
		SchemaVersion: payload.SchemaVersion,
		PeerCount:     payload.rowCount("peer"),
		UserCount:     payload.rowCount("user"),
		Kind:          kind,
		OperatorID:    operatorID,
	}
	if _, err := s.backups.Record(ctx, rec); err != nil {
		return nil, err
	}
	if err := s.prune(ctx); err != nil {
		s.log.Warn("prune old backups failed", "error", err)
	}
	return rec, nil
}

// uniqueFilename 生成不会与已有备份重名的文件名。
//
// 时间戳只精确到秒，而「手动点两次」「定时任务与手动同时触发」都会落在同一秒内，
// 此时两份备份会写同一个文件：先写的那份内容被覆盖，两条索引却指向同一路径，
// 恢复时拿到的是另一份数据。故命中重名时追加序号。
func uniqueFilename(dir string, now time.Time, kind string) string {
	base := fmt.Sprintf("wgpanel-%s-%s", now.Format("20060102T150405Z"), kind)
	ext := ".wgbak"
	for i := 0; ; i++ {
		name := base + ext
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name
		}
	}
}

// rowCount 统计某表行数。
func (p *Payload) rowCount(table string) int {
	for _, t := range p.Tables {
		if t.Name == table {
			return len(t.Rows)
		}
	}
	return 0
}

// List 返回备份索引。
func (s *Service) List(ctx context.Context) ([]domain.BackupRecord, error) {
	return s.backups.List(ctx)
}

// Delete 删除一份备份：先删索引再删文件。
//
// 顺序如此是因为文件删除可能因权限失败，此时索引还在，
// 用户仍能在界面上看到这条记录并重试；反过来则会留下看不见的孤儿文件。
func (s *Service) Delete(ctx context.Context, filename string) error {
	path, err := s.Path(ctx, filename)
	if err != nil {
		return err
	}
	items, err := s.backups.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, rec := range items {
		if rec.Filename != filename {
			continue
		}
		found = true
		if err := s.backups.Delete(ctx, rec.ID); err != nil {
			return err
		}
	}
	if !found {
		return domain.ErrNotFound("backup %q not found", filename)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup: remove file: %w", err)
	}
	return nil
}

// Path 返回备份文件路径，同时校验文件名不越界。
func (s *Service) Path(ctx context.Context, filename string) (string, error) {
	dir, err := s.backupDir(ctx)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(filename)
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return "", domain.ErrInvalid("illegal backup filename")
	}
	return filepath.Join(dir, name), nil
}

// Read 读取并校验一个备份，返回明文载荷。
func (s *Service) Read(ctx context.Context, filename string) (*Payload, error) {
	path, err := s.Path(ctx, filename)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, domain.ErrNotFound("backup file not found")
		}
		return nil, fmt.Errorf("backup: read file: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, domain.ErrInvalid("backup file is corrupted")
	}
	if env.FormatVersion != FormatVersion {
		return nil, domain.ErrInvalid("unsupported backup format version %d", env.FormatVersion)
	}
	sum := sha256.Sum256([]byte(env.Payload))
	if hex.EncodeToString(sum[:]) != env.SHA256 {
		return nil, domain.ErrInvalid("backup checksum mismatch")
	}

	plain, err := s.cipher.Decrypt(env.Payload)
	if err != nil {
		return nil, domain.ErrInvalid("backup cannot be decrypted with the current master key")
	}
	var payload Payload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		return nil, domain.ErrInvalid("backup payload is corrupted")
	}
	return &payload, nil
}

// Restore 用备份覆盖当前配置与身份数据。
//
// 保留 schema_migration 与流量时序表：前者属于运行结构，后者是历史统计。
// 恢复语义是「回到当时的配置」，不是「清空这台机器的所有历史」。
func (s *Service) Restore(ctx context.Context, filename string) error {
	payload, err := s.Read(ctx, filename)
	if err != nil {
		return err
	}
	version, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if payload.SchemaVersion > version {
		return domain.ErrConflict("backup was created by a newer version (schema %d > %d)",
			payload.SchemaVersion, version)
	}
	// 备份里的表必须是已知表名，否则可能来自被篡改的文件。
	allowed := make(map[string]bool, len(TableNames))
	for _, name := range TableNames {
		allowed[name] = true
	}
	for _, t := range payload.Tables {
		if !allowed[t.Name] {
			return domain.ErrInvalid("backup contains an unexpected table %q", t.Name)
		}
	}

	return s.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, t := range payload.Tables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.Name); err != nil {
				return fmt.Errorf("backup: clear %s: %w", t.Name, err)
			}
			for _, row := range t.Rows {
				if err := insertRow(ctx, tx, t, row); err != nil {
					return fmt.Errorf("backup: restore %s: %w", t.Name, err)
				}
			}
		}
		return nil
	})
}

// insertRow 按转储中记录的列名写回一行。
func insertRow(ctx context.Context, tx *sql.Tx, t TableDump, row []any) error {
	if len(t.Columns) == 0 || len(row) != len(t.Columns) {
		return fmt.Errorf("backup: row/column count mismatch for %s", t.Name)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(row)), ",")
	stmt := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		t.Name, strings.Join(t.Columns, ","), placeholders)
	_, err := tx.ExecContext(ctx, stmt, row...)
	return err
}

// export 导出当前数据为备份载荷。
func (s *Service) export(ctx context.Context) (*Payload, error) {
	payload := &Payload{FormatVersion: FormatVersion, CreatedAt: time.Now().UTC()}

	version, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	payload.SchemaVersion = version

	for _, name := range TableNames {
		columns, rows, err := s.store.DumpTable(ctx, name)
		if err != nil {
			return nil, err
		}
		payload.Tables = append(payload.Tables, TableDump{Name: name, Columns: columns, Rows: rows})
	}
	return payload, nil
}

// backupDir 解析备份目录，确保它位于数据目录内。
func (s *Service) backupDir(ctx context.Context) (string, error) {
	cfg, err := s.settings.LoadSettings(ctx)
	if err != nil {
		return "", err
	}
	name := cfg.BackupDir
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", domain.ErrInvalid("backupDir must be a plain directory name")
	}
	dir := filepath.Join(s.dataDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("backup: create dir: %w", err)
	}
	return dir, nil
}

// prune 按保留策略删除最旧的备份文件与索引。
func (s *Service) prune(ctx context.Context) error {
	cfg, err := s.settings.LoadSettings(ctx)
	if err != nil {
		return err
	}
	stale, err := s.backups.TrimBackupIndex(ctx, cfg.BackupRetentionCount)
	if err != nil {
		return err
	}
	dir, err := s.backupDir(ctx)
	if err != nil {
		return err
	}
	for _, rec := range stale {
		if err := os.Remove(filepath.Join(dir, rec.Filename)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := s.backups.Delete(ctx, rec.ID); err != nil {
			return err
		}
	}
	return nil
}
