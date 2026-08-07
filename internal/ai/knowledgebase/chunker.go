package knowledgebase

// ChunkText 按 rune 切片（中文友好，按字符数非 token 估算）。
//
// 参数：
//   - content：原文
//   - size：每片字符数（<=0 默认 500）
//   - overlap：片间重叠字符数（<0 或 >=size 默认 size/5，保证步进 > 0 不死循环）
//
// 返回切片列表（空原文返 nil）。步进 = size - overlap，末尾不足 size 单独成片。
func ChunkText(content string, size, overlap int) []string {
	if content == "" {
		return nil
	}
	if size <= 0 {
		size = 500
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 5
	}
	step := size - overlap
	if step <= 0 {
		step = 1
	}
	runes := []rune(content)
	var chunks []string
	for i := 0; i < len(runes); {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
		if end >= len(runes) {
			break
		}
		i += step
	}
	return chunks
}
