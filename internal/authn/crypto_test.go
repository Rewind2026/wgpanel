package authn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	const password = "S3cure!Admin2026"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(password, encoded) {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if VerifyPassword("S3cure!Admin2027", encoded) {
		t.Fatal("VerifyPassword accepted a wrong password")
	}
}

func TestCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	secret := "aGVsbG8td29yZGRfMTIzNDU2Nzg5"
	encrypted, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == secret {
		t.Fatal("Encrypt did not transform the plaintext")
	}
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != secret {
		t.Fatalf("Decrypt = %q, want %q", decrypted, secret)
	}

	if got, err := c.Decrypt(""); err != nil || got != "" {
		t.Fatalf("empty ciphertext should round-trip to empty, got (%q, %v)", got, err)
	}
}

func TestRandomTokenIsURLSafe(t *testing.T) {
	token, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	if len(token) < 32 {
		t.Fatalf("token too short: %d", len(token))
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' {
			t.Fatalf("token contains unsafe char %q", r)
		}
	}
}

// TestMasterKeyPresentGate 锁定「库里已有密文但密钥文件丢了」必须报错。
//
// 静默生成新密钥是这个检查存在的唯一理由：那样面板会正常启动，
// 但库里所有私钥都变成永久解不开的密文——用户侧表现是「服务看着好好的，
// 就是谁都连不上」，排查成本极高。
func TestMasterKeyPresentGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")

	// 全新安装：库里没有密文，缺文件是正常的，走生成路径。
	if err := EnsureMasterKeyPresent(path, false); err != nil {
		t.Fatalf("空库不应拦截：%v", err)
	}
	if _, err := LoadOrCreateMasterKey(path); err != nil {
		t.Fatalf("首次应能生成主密钥：%v", err)
	}

	// 密钥在：无论库里有没有密文都放行。
	if err := EnsureMasterKeyPresent(path, true); err != nil {
		t.Fatalf("密钥存在时不应拦截：%v", err)
	}

	// 关键场景：库里已有密文，密钥文件没了。
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	err := EnsureMasterKeyPresent(path, true)
	if !errors.Is(err, ErrMasterKeyLost) {
		t.Fatalf("密钥缺失且库中有密文时应返回 ErrMasterKeyLost，得到 %v", err)
	}
}

// TestMasterKeyFileIsReused 确认重启不会换密钥，否则历史密文全部失效。
func TestMasterKeyFileIsReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey: %v", err)
	}
	second, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("同一密钥文件不应生成不同的主密钥")
	}
}
