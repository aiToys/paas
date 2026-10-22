// Package skillassets 嵌入官方 Skill 源文件，保证在线与离线交付使用同一份内容。
package skillassets

import "embed"

// Files 包含只读目录元数据及标准 SKILL.md，供 AI 领域解析。
//
//go:embed catalog.json */SKILL.md
var Files embed.FS
