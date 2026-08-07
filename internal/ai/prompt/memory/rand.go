package memory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
