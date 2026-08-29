package crypto

import (
	"bytes"
	"strings"
	"testing"
)

// 往返：加密后带前缀，解密还原。
func TestRoundTrip(t *testing.T) {
	c, err := NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("sk-明文密码123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, CipherPrefix) {
		t.Fatalf("密文缺前缀: %q", enc)
	}
	if strings.Contains(enc, "明文密码") {
		t.Fatal("密文包含明文")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "sk-明文密码123" {
		t.Fatalf("解密不符: %q", dec)
	}
}

// 同明文两次加密 nonce 不同 → 密文不同（无确定性泄漏）。
func TestNonceRandomness(t *testing.T) {
	c, _ := NewFromHex(strings.Repeat("ab", 32))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("两次密文相同（nonce 复用）")
	}
}

// Decrypt 无前缀 = 明文原样返回（存量兼容核心）。
func TestDecryptPlaintextPassthrough(t *testing.T) {
	c, _ := NewFromHex(strings.Repeat("ab", 32))
	out, err := c.Decrypt("存量明文值")
	if err != nil || out != "存量明文值" {
		t.Fatalf("明文透传失败: %q %v", out, err)
	}
}

// 错 key 解密报错（GCM auth 失败）。
func TestWrongKeyFails(t *testing.T) {
	c1, _ := NewFromHex(strings.Repeat("ab", 32))
	c2, _ := NewFromHex(strings.Repeat("cd", 32))
	enc, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("错 key 解密应报错")
	}
}

// 非 32 字节 key 拒建。
func TestInvalidKeyLength(t *testing.T) {
	if _, err := New(bytes.Repeat([]byte{1}, 16)); err == nil {
		t.Fatal("16 字节应拒绝")
	}
	if _, err := NewFromHex("abcd"); err == nil {
		t.Fatal("短 hex 应拒绝")
	}
	if _, err := NewFromHex("zz"); err == nil {
		t.Fatal("非法 hex 应拒绝")
	}
}

// NewFromEnv：空 env → (nil, nil) 明文模式；合法 → cipher。
func TestNewFromEnv(t *testing.T) {
	t.Setenv("K", "")
	c, err := NewFromEnv("K")
	if err != nil || c != nil {
		t.Fatalf("空 env 应返 nil cipher: %v %v", c, err)
	}
	t.Setenv("K", strings.Repeat("ab", 32))
	c, err = NewFromEnv("K")
	if err != nil || c == nil {
		t.Fatalf("合法 env 应返 cipher: %v %v", c, err)
	}
	t.Setenv("K", "short")
	if _, err := NewFromEnv("K"); err == nil {
		t.Fatal("非法 env 应报错")
	}
}
