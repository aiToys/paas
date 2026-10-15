// unified diff 轻量解析（PR 评审展示用，不引外部 diff 库）。
// 按 "diff --git" 分文件；行首 +/=add、-/=del、@@/index/元信息=meta。

export interface DiffLine { type: 'add' | 'del' | 'ctx' | 'meta'; text: string }
export interface DiffFile { path: string; lines: DiffLine[]; adds: number; dels: number }

// parseDiff 解析 unified diff 文本为文件分组。
export function parseDiff(text: string): DiffFile[] {
  const files: DiffFile[] = []
  let cur: DiffFile | null = null
  for (const raw of text.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      // "diff --git a/path b/path" 取 b 侧（新文件路径）
      const m = raw.match(/^diff --git a\/(.+) b\/(.+)$/)
      cur = { path: m ? m[2] : raw, lines: [], adds: 0, dels: 0 }
      files.push(cur)
      continue
    }
    if (!cur) continue
    if (raw.startsWith('+++') || raw.startsWith('---') || raw.startsWith('@@') ||
        raw.startsWith('index ') || raw.startsWith('new file') || raw.startsWith('deleted file') ||
        raw.startsWith('old mode') || raw.startsWith('new mode') || raw.startsWith('Binary') ||
        raw.startsWith('similarity') || raw.startsWith('rename ') || raw.startsWith('copy ') ||
        raw.startsWith('\\') || raw.startsWith('GIT binary patch')) {
      cur.lines.push({ type: 'meta', text: raw })
    } else if (raw.startsWith('+')) {
      cur.lines.push({ type: 'add', text: raw })
      cur.adds++
    } else if (raw.startsWith('-')) {
      cur.lines.push({ type: 'del', text: raw })
      cur.dels++
    } else {
      cur.lines.push({ type: 'ctx', text: raw.replace(/^ /, '') })
    }
  }
  return files
}
