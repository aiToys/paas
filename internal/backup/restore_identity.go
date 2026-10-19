package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RestoreID 在租户与源快照内固定一次用户操作；重复请求复用记录与目标实例。
func RestoreID(tenant, source, key string) string {
	b, _ := json.Marshal([]string{tenant, source, key})
	h := sha256.Sum256(b)
	return "bk-r-" + hex.EncodeToString(h[:16])
}

func RestoreTargetID(operation string) string { return "ds-" + operation }

func ValidChecksum(s string) bool {
	value, err := hex.DecodeString(s)
	return err == nil && len(value) == sha256.Size
}
