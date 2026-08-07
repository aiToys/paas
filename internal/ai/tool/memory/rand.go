package memory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// randID 生成随机 ID（crypto/rand 防可预测；fallback 时间戳防 panic）。
func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
