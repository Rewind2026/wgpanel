package authn

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2 参数：OWASP 推荐基线。
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrInvalidCiphertext 表示密文格式非法或密钥不匹配。
var ErrInvalidCiphertext = errors.New("invalid ciphertext")

// Cipher 负责对称加密存储 WireGuard 私钥等敏感数据。
type Cipher struct {
	aead cipher.AEAD
}

// LoadOrCreateMasterKey 从配置读取主密钥；为空时在路径上生成并持久化。
// 已有密钥文件且未提供环境变量时，直接复用，保证重启后仍能解密历史数据。
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decErr := base64.StdEncoding.DecodeString(string(raw))
		if decErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("authn: master key file %s is corrupted", path)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("authn: read master key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("authn: generate master key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("authn: persist master key: %w", err)
	}
	return key, nil
}

// ErrMasterKeyLost 表示「数据目录里已有加密数据，但主密钥不见了」。
//
// 这是一个必须让进程起不来的状态。静默生成一把新密钥看起来很自然——
// 首次启动本来就要生成——但它的后果是库里所有 WireGuard 私钥、
// 服务端私钥、用户令牌全部变成永久无法解开的密文：
// 界面正常、接口正常、就是一台连不上任何客户端的僵尸面板，
// 而管理员直到用户报修才会发现。
//
// 触发路径很常见：把 wgpanel.db 从备份/迁移里恢复回来、却漏了 master.key；
// 或者挂载卷时只映射了 db 文件。两者的表现都是「面板能开，就是没人能连」。
var ErrMasterKeyLost = errors.New("authn: master key is missing but the database already holds encrypted secrets")

// EnsureMasterKeyPresent 在数据库已有加密数据时校验主密钥是否还在。
//
// hasSecrets 由调用方给出（通常是「server_config 表里已有私钥」），
// 而不是在这里去开数据库——authn 不该知道数据的存放方式。
func EnsureMasterKeyPresent(path string, hasSecrets bool) error {
	if !hasSecrets {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("authn: stat master key: %w", err)
	}
	return fmt.Errorf("%w: %s", ErrMasterKeyLost, path)
}

// NewCipher 由 32 字节主密钥构造 AES-256-GCM。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("authn: master key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("authn: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("authn: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 返回 nonce||ciphertext 的 base64 字符串。空串原样返回，便于「未设置」语义。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("authn: nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 是 Encrypt 的逆操作。
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrInvalidCiphertext
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(plain), nil
}

// HashPassword 使用 argon2id 生成带参数的密码哈希。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("authn: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 以常量时间比对密码。
func VerifyPassword(password, encoded string) bool {
	salt, want, timeCost, mem, threads, ok := parseArgon2Hash(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, mem, threads, uint32(len(want)))
	return subtleEqual(got, want)
}

// parseArgon2Hash 解析 $argon2id$v=19$m=..,t=..,p=..$salt$key 格式。
// 手动解析而非 Sscanf：Sscanf 对 $ 与逗号的处理不可靠，且不校验字段完整性。
func parseArgon2Hash(encoded string) (salt, key []byte, timeCost, memory uint32, threads uint8, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, false
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return nil, nil, 0, 0, 0, false
	}

	params := map[string]uint64{}
	for _, kv := range strings.Split(parts[3], ",") {
		name, value, found := strings.Cut(kv, "=")
		if !found {
			return nil, nil, 0, 0, 0, false
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, nil, 0, 0, 0, false
		}
		params[name] = n
	}
	if len(params) != 3 || params["m"] == 0 || params["t"] == 0 || params["p"] == 0 {
		return nil, nil, 0, 0, 0, false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, false
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return nil, nil, 0, 0, 0, false
	}
	return salt, key, uint32(params["t"]), uint32(params["m"]), uint8(params["p"]), true
}

// RandomToken 生成 URL 安全的随机令牌。
func RandomToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("authn: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// LoadOrCreateSecret 复用或生成持久化的运行时密钥。
func LoadOrCreateSecret(path string, bytesLen int) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		return raw, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("authn: read secret: %w", err)
	}
	token, err := RandomToken(bytesLen)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return nil, fmt.Errorf("authn: persist secret: %w", err)
	}
	return []byte(token), nil
}
