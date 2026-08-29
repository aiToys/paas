// conn_crypto.go DataService Connection 字段级静态加密（持久层接缝，纯函数）。
// 敏感字段集合与掩码共用同一真源 dataservice.MaskKeys（password/secretKey/token/
// api_key/master_key/uri），host/port/user/database 明文保留（排障可读）。
//
// 为什么在 store 层而非 Repository 装饰器：managed 模式 handler 清空 Connection、
// store 内 FillConnection 生成凭证——装饰器先跑时无值可加密，包在 ApplyRepo 内侧
// 还会把密文投影进 CRD 毁掉引擎凭证。加密必须发生在 FillConnection 之后、
// marshalSpec 落库之前（写），以及 Scan 读出之后（读），即 store 持久层接缝。
//
// 无前缀密文透传（存量明文零迁移兼容）；nil cipher 双向透传（dev 明文模式）。
package pg

import (
	"github.com/aitoys/paas/internal/crypto"
	"github.com/aitoys/paas/internal/dataservice"
)

// encryptConnection 加密 Connection 敏感字段（写路径，落库前调）。
// 纯函数：返回新 map，不修改入参。加密失败保留原值
// （与 nil cipher 透传一致，不阻断主流程；读侧无前缀仍原样可读）。
func encryptConnection(c *crypto.Cipher, conn map[string]string) map[string]string {
	out := make(map[string]string, len(conn))
	for k, v := range conn {
		if dataservice.MaskKeys[k] && v != "" {
			if enc, err := c.Encrypt(v); err == nil {
				v = enc
			}
		}
		out[k] = v
	}
	return out
}

// decryptConnection 解密 Connection 敏感字段（读路径，Scan 读出后调）。
// 纯函数：返回新 map；无前缀存量明文透传，解密失败保留原值。
func decryptConnection(c *crypto.Cipher, conn map[string]string) map[string]string {
	out := make(map[string]string, len(conn))
	for k, v := range conn {
		if dataservice.MaskKeys[k] && v != "" {
			if plain, err := c.Decrypt(v); err == nil {
				v = plain
			}
		}
		out[k] = v
	}
	return out
}
