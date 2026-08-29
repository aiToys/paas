// Package crypto 提供敏感字段静态加密（AES-256-GCM，零外部依赖）。
// 密文带前缀 "enc:v1:"（版本位，未来算法升级留路）；Decrypt 无前缀原样返回，
// 存量明文数据零迁移兼容——升级部署后旧数据照常可读，新写入逐步密文化。
//
// nil Cipher = 明文兼容模式（Encrypt/Decrypt 原样透传），dev 未配 master key 时使用。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CipherPrefix 密文前缀（含版本）。
const CipherPrefix = "enc:v1:"

// keyLen AES-256 字节长度。
const keyLen = 32

// Cipher AES-256-GCM 加解密器。nil 指针 = 明文模式。
type Cipher struct{ aead cipher.AEAD }

// New 用 32 字节 raw key 构造（其他长度报错）。
func New(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("master key 须为 %d 字节，实际 %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// NewFromHex 用 64 位 hex 字符串构造（env 注入的标准形态）。
func NewFromHex(hexKey string) (*Cipher, error) {
	key, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, fmt.Errorf("master key 非合法 hex: %w", err)
	}
	return New(key)
}

// NewFromEnv 读 env：空值返 (nil, nil) 明文模式；非空则须合法（报错）。
// 调用方据此决定明文兼容或拒绝启动（生产）。
func NewFromEnv(name string) (*Cipher, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil, nil
	}
	return NewFromHex(v)
}

// Encrypt 加密：nil cipher 明文透传（dev 模式）；输出 CipherPrefix + base64(nonce|ct)。
// GCM 随机 nonce——同明文每次密文不同，无语义泄漏。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if c == nil {
		return plain, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return CipherPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密：nil cipher / 无前缀（存量明文）原样返回；密文校验失败报错。
func (c *Cipher) Decrypt(s string) (string, error) {
	if c == nil || !strings.HasPrefix(s, CipherPrefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, CipherPrefix))
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（key 不匹配或数据损坏）: %w", err)
	}
	return string(plain), nil
}
