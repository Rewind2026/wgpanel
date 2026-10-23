package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// testKey 是确定性的 32 字节主密钥，便于跨用例复用同一份 fixture。
var testKey = []byte("0123456789abcdef0123456789abcdef")

type fixture struct {
	svc    *Service
	store  *store.Store
	cipher *authn.Cipher
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	c, err := authn.NewCipher(testKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{
		svc:    New(st, c, dir, log),
		store:  st,
		cipher: c,
		dir:    dir,
	}
}

// seedPeer 写入一个带私钥的 Peer。私钥必须是密文，否则恢复后客户端拿不到凭据。
func (f *fixture) seedPeer(t *testing.T, name, privKey string) int64 {
	t.Helper()
	sealed, err := f.cipher.Encrypt(privKey)
	if err != nil {
		t.Fatalf("encrypt private key: %v", err)
	}
	id, err := f.store.PeerRepo().Create(context.Background(), &domain.Peer{
		Name:       name,
		PublicKey:  "dpmLZIKxpezJZNlZwlv6pLg8goGW8C6jasftaRxv8yQ=",
		PrivateKey: sealed,
		Address:    name + "-addr",
		AllowedIPs: "10.8.0.2/32",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	return id
}

func (f *fixture) seedUser(t *testing.T, username, hash string) {
	t.Helper()
	if _, err := f.store.UserRepo().Create(context.Background(), &domain.User{
		Username: username, PasswordHash: hash, Role: domain.RoleAdmin, Enabled: true,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func TestBackupRoundTripPreservesSecretsAndHistory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	const privKey = "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI="
	const pwHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$ZG9uZS1oYXNo"
	f.seedPeer(t, "laptop", privKey)
	f.seedUser(t, "admin", pwHash)
	if err := f.store.SettingRepo().SaveSettings(ctx, domain.DefaultSettings()); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	rec, err := f.svc.Create(ctx, "manual", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.PeerCount != 1 || rec.UserCount != 1 {
		t.Fatalf("索引应记录 1 peer / 1 user，实际 %d / %d", rec.PeerCount, rec.UserCount)
	}

	// 破坏当前数据：验证恢复确实是「覆盖」而不是「合并」。
	if err := f.store.PeerRepo().Delete(ctx, 1); err != nil {
		t.Fatalf("delete peer: %v", err)
	}
	// 用户表不进仓储的删除方法（单人部署没有删号场景），这里直接写 SQL。
	if _, err := f.store.DB().ExecContext(ctx, `DELETE FROM user WHERE id = 1`); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if err := f.svc.Restore(ctx, rec.Filename); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	peers, err := f.store.PeerRepo().List(ctx)
	if err != nil {
		t.Fatalf("list peers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("恢复后应有 1 个 Peer，实际 %d", len(peers))
	}
	// 关键：私钥必须原样回来。领域模型里它是 json:"-"，
	// 如果备份改成对象 JSON 往返，这里就会变成空串，所有客户端全部失效。
	if peers[0].PrivateKey == "" {
		t.Fatal("恢复后私钥密文丢失")
	}
	got, err := f.cipher.Decrypt(peers[0].PrivateKey)
	if err != nil {
		t.Fatalf("decrypt restored private key: %v", err)
	}
	if got != privKey {
		t.Errorf("私钥应原样往返，实际 %q", got)
	}

	// 密码哈希同理：丢了它等于所有人被强制登出。
	restored, err := f.store.UserRepo().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("get restored user: %v", err)
	}
	if restored.PasswordHash != pwHash {
		t.Errorf("密码哈希应原样往返，实际 %q", restored.PasswordHash)
	}
}

func TestRestoreKeepsTrafficHistory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")

	// 先造一条流量记录，恢复后它必须还在。
	if _, err := f.store.PeerRepo().AddQuotaUsage(ctx, 1, 4096); err != nil {
		t.Fatalf("AddQuotaUsage: %v", err)
	}
	rec, err := f.svc.Create(ctx, "manual", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.svc.Restore(ctx, rec.Filename); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	peers, _ := f.store.PeerRepo().List(ctx)
	// 恢复回到「当时的配置」，但流量统计属于历史，不该被清空。
	if peers[0].QuotaUsedBytes != 4096 {
		t.Errorf("恢复不应清空流量统计，实际 %d", peers[0].QuotaUsedBytes)
	}
}

func TestBackupFileIsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const privKey = "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI="
	f.seedPeer(t, "laptop", privKey)

	rec, err := f.svc.Create(ctx, "manual", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "backups", rec.Filename))
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	// 备份文件泄漏到别处（对象存储、工单附件）时不应直接暴露私钥。
	if strings.Contains(string(raw), privKey) {
		t.Error("备份文件里出现了明文私钥")
	}
	if !strings.Contains(string(raw), `"cipher":"aes-256-gcm"`) {
		t.Error("备份文件缺少加密标记")
	}
}

func TestReadRejectsTamperedAndForeignBackups(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")
	rec, err := f.svc.Create(ctx, "manual", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(f.dir, "backups", rec.Filename)

	t.Run("checksum mismatch", func(t *testing.T) {
		var env envelope
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		env.Payload = "tampered-payload"
		body, _ := json.Marshal(env)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := f.svc.Read(ctx, rec.Filename); err == nil {
			t.Error("校验和不匹配时必须拒绝")
		}
	})

	t.Run("wrong master key", func(t *testing.T) {
		other, err := authn.NewCipher([]byte("ffffffffffffffffffffffffffffffff"))
		if err != nil {
			t.Fatalf("new cipher: %v", err)
		}
		foreign := New(f.store, other, f.dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
		// 用正确密钥重新生成一份，再用别的密钥读。
		f2 := newFixture(t)
		f2.seedPeer(t, "phone", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")
		good, err := f2.svc.Create(ctx, "manual", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(f2.dir, "backups", good.Filename))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(f.dir, "backups"), 0o700); err != nil {
			t.Fatalf("mkdir backups: %v", err)
		}
		if err := os.WriteFile(filepath.Join(f.dir, "backups", good.Filename), body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := foreign.Read(ctx, good.Filename); err == nil {
			t.Error("换主密钥后必须拒绝解密旧备份")
		}
	})

	t.Run("unsupported format version", func(t *testing.T) {
		body, _ := json.Marshal(envelope{FormatVersion: 99, SHA256: "x", Payload: "y"})
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := f.svc.Read(ctx, rec.Filename); err == nil {
			t.Error("未知格式版本必须拒绝")
		}
	})
}

func TestRestoreRejectsUnknownTable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")

	payload := &Payload{
		FormatVersion: FormatVersion,
		SchemaVersion: 1,
		Tables:        []TableDump{{Name: "sqlite_master", Columns: []string{"sql"}, Rows: [][]any{{"DROP TABLE peer"}}}},
	}
	raw, _ := json.Marshal(payload)
	sealed, err := f.cipher.Encrypt(string(raw))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	sum := sha256.Sum256([]byte(sealed))
	body, _ := json.Marshal(envelope{
		FormatVersion: FormatVersion, CreatedAt: 1, Cipher: "aes-256-gcm",
		SHA256: hex.EncodeToString(sum[:]), Payload: sealed,
	})
	name := "wgpanel-evil-manual.wgbak"
	if err := os.MkdirAll(filepath.Join(f.dir, "backups"), 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "backups", name), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 备份文件是可被替换的输入，表名必须白名单校验，
	// 否则一个精心构造的文件就能让恢复语句打到任意表上。
	if err := f.svc.Restore(ctx, name); err == nil {
		t.Error("含未知表的备份必须拒绝恢复")
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	for _, name := range []string{
		"../../etc/passwd", "..", "sub/dir.wgbak", `..\windows`, "",
	} {
		if _, err := f.svc.Path(ctx, name); err == nil {
			t.Errorf("非法文件名 %q 应被拒绝", name)
		}
	}
}

func TestPruneKeepsRetentionCount(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	cfg := domain.DefaultSettings()
	cfg.BackupRetentionCount = 2
	if err := f.store.SettingRepo().SaveSettings(ctx, cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")

	var created []string
	for range 4 {
		rec, err := f.svc.Create(ctx, "manual", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		created = append(created, rec.Filename)
	}

	items, err := f.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应只保留 2 份，实际 %d", len(items))
	}
	// 保留最新的两份，最旧的两份的索引和文件都应消失。
	keep := map[string]bool{}
	for _, it := range items {
		keep[it.Filename] = true
	}
	for _, name := range created {
		_, statErr := os.Stat(filepath.Join(f.dir, "backups", name))
		if keep[name] && statErr != nil {
			t.Errorf("保留的备份文件 %q 不应被删除", name)
		}
		if !keep[name] && statErr == nil {
			t.Errorf("过期备份文件 %q 应被删除", name)
		}
	}
}

// 同一秒内连续创建多份备份不能互相覆盖。
//
// 时间戳只有秒精度，而手动连点两下、定时任务与手动撞车都会发生在一秒内。
// 若文件名相同，先写的那份内容被静默覆盖，两条索引指向同一路径，
// 恢复时拿到的是另一份数据——这是静默的数据错误，不是报错。
func TestBackupFilenamesDoNotCollide(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")

	seen := map[string]bool{}
	for i := range 3 {
		rec, err := f.svc.Create(ctx, "manual", nil)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		if seen[rec.Filename] {
			t.Fatalf("文件名重复：%s", rec.Filename)
		}
		seen[rec.Filename] = true
	}

	// 每份备份都要能被独立读回，且各自有唯一内容。
	sums := map[string]bool{}
	for name := range seen {
		payload, err := f.svc.Read(ctx, name)
		if err != nil {
			t.Fatalf("Read %s: %v", name, err)
		}
		if payload.rowCount("peer") != 1 {
			t.Errorf("%s 应含 1 个 peer，实际 %d", name, payload.rowCount("peer"))
		}
		sums[name] = true
	}
	if len(sums) != 3 {
		t.Errorf("应有 3 份独立可读的备份，实际 %d", len(sums))
	}
}

func TestDeleteRemovesIndexAndFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seedPeer(t, "laptop", "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=")
	rec, err := f.svc.Create(ctx, "manual", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.svc.Delete(ctx, rec.Filename); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	items, _ := f.svc.List(ctx)
	if len(items) != 0 {
		t.Errorf("删除后索引应为空，实际 %d 条", len(items))
	}
	if _, err := os.Stat(filepath.Join(f.dir, "backups", rec.Filename)); !os.IsNotExist(err) {
		t.Error("删除后备份文件应不存在")
	}
	if err := f.svc.Delete(ctx, rec.Filename); err == nil {
		t.Error("重复删除不存在的备份应报错")
	}
}
