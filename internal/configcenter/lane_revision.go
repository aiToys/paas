package configcenter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// LaneRevision 固定预览时的内容与更新时间；同数量改值也会使预览失效。
func LaneRevision(overrides []LaneOverride) string {
	rows := append([]LaneOverride{}, overrides...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	b, _ := json.Marshal(rows)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
