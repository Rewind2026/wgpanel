package authn

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// Claims 是访问令牌的载荷。
type Claims struct {
	UserID   int64       `json:"uid"`
	Username string      `json:"usr"`
	Role     domain.Role `json:"rol"`
	// Ver 是签发时的 token_version。
	// 服务端每次鉴权都比对库里的当前值：一旦管理员改密、停用账号或强制下线，
	// 该值被提升，此前签发的令牌（ver 落后）立刻全部作废。
	Ver int64 `json:"ver"`
	jwt.RegisteredClaims
}

// TokenIssuer 负责签发与校验访问令牌。
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer 构造签发器。secret 需为高熵随机值。
func NewTokenIssuer(secret []byte, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: secret, ttl: ttl}
}

// TTL 返回访问令牌有效期。
func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

// Issue 签发访问令牌。
func (t *TokenIssuer) Issue(u *domain.User, now time.Time) (string, error) {
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		Ver:      u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			Issuer:    "wgpanel",
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
			ID:        fmt.Sprintf("%d-%d", u.ID, now.UnixNano()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// Parse 校验并解析访问令牌。
func (t *TokenIssuer) Parse(tokenStr string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", tok.Header["alg"])
		}
		return t.secret, nil
	}, jwt.WithIssuer("wgpanel"), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, domain.ErrUnauthorized("token expired")
		}
		return nil, domain.ErrUnauthorized("invalid token")
	}
	if claims.Role == "" || !claims.Role.Valid() {
		return nil, domain.ErrUnauthorized("invalid token role")
	}
	return &claims, nil
}

// Principal 是请求上下文中的调用方身份。
type Principal struct {
	UserID   int64
	Username string
	Role     domain.Role
}

// Can 判断主体是否具备某权限。
func (p *Principal) Can(perm domain.Permission) bool {
	if p == nil {
		return false
	}
	return p.Role.Has(perm)
}

// CanAll 判断主体是否同时具备多个权限。
func (p *Principal) CanAll(perms ...domain.Permission) bool {
	for _, perm := range perms {
		if !p.Can(perm) {
			return false
		}
	}
	return true
}
