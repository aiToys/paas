package pg

import (
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/crypto"
)

// cryptoTestCipher 测试 cipher（64 位 hex）。
func cryptoTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// cryptoSampleConn 样例连接信息（敏感 + 非敏感混合）。
func cryptoSampleConn() map[string]string {
	return map[string]string{
		"password": "p@ss",
		"host":     "db.paas.svc.cluster.local",
		"port":     "5432",
	}
}

// 用例 1：encryptConnection 仅加密敏感字段，host/port 原样明文保留。
func TestEncryptConnectionSensitiveFieldsOnly(t *testing.T) {
	out := encryptConnection(cryptoTestCipher(t), cryptoSampleConn())
	if !strings.HasPrefix(out["password"], crypto.CipherPrefix) {
		t.Fatalf("password 未加密: %q", out["password"])
	}
	if strings.Contains(out["password"], "p@ss") {
		t.Fatal("password 密文含明文")
	}
	if out["host"] != "db.paas.svc.cluster.local" {
		t.Fatalf("host 应明文保留: %q", out["host"])
	}
	if out["port"] != "5432" {
		t.Fatalf("port 应明文保留: %q", out["port"])
	}
}

// 用例 2：decryptConnection 还原密文，非敏感字段不动。
func TestDecryptConnectionRoundTrip(t *testing.T) {
	c := cryptoTestCipher(t)
	enc := encryptConnection(c, cryptoSampleConn())
	out := decryptConnection(c, enc)
	if out["password"] != "p@ss" {
		t.Fatalf("password 未解密: %q", out["password"])
	}
	if out["host"] != "db.paas.svc.cluster.local" || out["port"] != "5432" {
		t.Fatalf("非敏感字段不应被动: %v", out)
	}
}

// 用例 3：无前缀明文（存量数据）原样透出（零迁移兼容）。
func TestDecryptConnectionLegacyPlaintext(t *testing.T) {
	out := decryptConnection(cryptoTestCipher(t), map[string]string{"password": "legacy-plain"})
	if out["password"] != "legacy-plain" {
		t.Fatalf("存量明文应原样透出: %q", out["password"])
	}
}

// 用例 4：nil cipher（dev 明文模式）双向透传。
func TestConnectionNilCipherPassthrough(t *testing.T) {
	conn := cryptoSampleConn()
	enc := encryptConnection(nil, conn)
	if enc["password"] != "p@ss" {
		t.Fatalf("nil cipher 加密应透传: %q", enc["password"])
	}
	dec := decryptConnection(nil, enc)
	if dec["password"] != "p@ss" {
		t.Fatalf("nil cipher 解密应透传: %q", dec["password"])
	}
}

// 用例 5：Update 路径——先 FillConnection 重算再加密，密文解回后凭证完整
// （模拟 store.Update 的持久层接缝顺序：FillConnection 后 encrypt，读出 decrypt）。
func TestUpdatePathEncryptAfterFill(t *testing.T) {
	c := cryptoTestCipher(t)
	// FillConnection 重算产物含凭证与 host/port/uri（uri 整串属敏感字段）。
	filled := map[string]string{
		"user": "postgres", "password": "new-p@ss", "host": "h", "port": "5432",
		"database": "db", "uri": "postgres://postgres:new-p@ss@h:5432/db",
	}
	stored := encryptConnection(c, filled) // store 落库前
	// 密文断言：password 与 uri 均加密，user/host/port/database 明文。
	if !strings.HasPrefix(stored["password"], crypto.CipherPrefix) {
		t.Fatalf("password 未加密: %q", stored["password"])
	}
	if !strings.HasPrefix(stored["uri"], crypto.CipherPrefix) {
		t.Fatalf("uri 未加密: %q", stored["uri"])
	}
	if stored["user"] != "postgres" || stored["host"] != "h" {
		t.Fatalf("user/host 应明文保留: %v", stored)
	}
	// 读出解密：凭证完整还原（引擎注入消费点拿到明文）。
	got := decryptConnection(c, stored)
	if got["password"] != "new-p@ss" || got["uri"] != filled["uri"] {
		t.Fatalf("解密不完整: %v", got)
	}
}

// 用例 6（补充）：输入 map 不被原地修改（纯函数语义，防调用方副作用）。
func TestConnectionEncryptDoesNotMutateInput(t *testing.T) {
	conn := cryptoSampleConn()
	_ = encryptConnection(cryptoTestCipher(t), conn)
	if conn["password"] != "p@ss" {
		t.Fatalf("输入 map 被原地修改: %q", conn["password"])
	}
}
