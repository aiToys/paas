// Package auth 提供 console-admin 身份对接所需的密码登录与 JWT 能力。
// JWT 用 HMAC-SHA256 自实现（零外部依赖，仅标准库）；密码哈希用 bcrypt。
// 与 gateway.APIKeyAuth 并列：人机交互走密码登录换 JWT，程序化调用仍用 API Key。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token 类型。
const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

// 有效期。
const (
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 7 * 24 * time.Hour
)

// 校验错误。
var (
	ErrTokenMalformed = errors.New("token 格式错误")
	ErrTokenSignature = errors.New("token 签名无效")
	ErrTokenExpired   = errors.New("token 已过期")
	ErrTokenType      = errors.New("token 类型不符")
)

// Claims 是 JWT 的载荷。
type Claims struct {
	Sub    string   `json:"sub"`    // 用户 ID
	Tenant string   `json:"tenant"` // 租户 ID
	Roles  []string `json:"roles"`  // 角色名
	Typ    string   `json:"typ"`    // access|refresh
	Exp    int64    `json:"exp"`    // 过期 unix 秒
	Iat    int64    `json:"iat"`    // 签发 unix 秒
}

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

// Sign 用 secret 对 claims 签发 JWT。
func Sign(c Claims, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("jwt secret 为空")
	}
	now := time.Now()
	if c.Iat == 0 {
		c.Iat = now.Unix()
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(jwtHeader)) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	sig := signHS256(enc, secret)
	return enc + "." + sig, nil
}

// Parse 校验签名与 exp，返回 Claims。
func Parse(token, secret string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrTokenMalformed
	}
	enc := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(signHS256(enc, secret)), []byte(parts[2])) {
		return nil, ErrTokenSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrTokenMalformed
	}
	if time.Now().Unix() >= c.Exp {
		return nil, ErrTokenExpired
	}
	return &c, nil
}

// ParseType 校验签名+exp+期望 typ（access/refresh）。
func ParseType(token, secret, typ string) (*Claims, error) {
	c, err := Parse(token, secret)
	if err != nil {
		return nil, err
	}
	if c.Typ != typ {
		return nil, fmt.Errorf("%w: 期望 %s 实际 %s", ErrTokenType, typ, c.Typ)
	}
	return c, nil
}

func signHS256(data, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
