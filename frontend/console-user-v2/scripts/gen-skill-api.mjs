// 从 route registry 导出的完整契约提取 Skill 路径及引用闭包，避免夹带其他模块的类型漂移。
import { readFile, writeFile } from 'node:fs/promises'
import openapiTS, { astToString, COMMENT_HEADER } from 'openapi-typescript'

const source = process.argv[2] || 'http://localhost:8080/openapi.json'
const spec = /^https?:\/\//.test(source)
  ? await fetch(source).then((r) => { if (!r.ok) throw new Error(`契约读取失败：${r.status}`); return r.json() })
  : JSON.parse(await readFile(source, 'utf8'))
const paths = Object.fromEntries(Object.entries(spec.paths).filter(([path]) => path === '/api/skills' || path.startsWith('/api/skills/')))
const schemas = {}
function collect(value) {
  if (!value || typeof value !== 'object') return
  if (typeof value.$ref === 'string' && value.$ref.startsWith('#/components/schemas/')) {
    const name = value.$ref.split('/').at(-1)
    if (!(name in schemas)) {
      const schema = spec.components.schemas[name]
      if (!schema) throw new Error(`缺少 schema：${name}`)
      schemas[name] = schema
      collect(schema)
    }
  }
  for (const child of Object.values(value)) collect(child)
}
collect(paths)
if (!paths['/api/skills/templates']) throw new Error('契约尚未包含 Skill 模板接口')
const scoped = { ...spec, paths, components: { schemas, securitySchemes: spec.components.securitySchemes } }
const ast = await openapiTS(scoped)
await writeFile(new URL('../src/api/skills.gen.ts', import.meta.url), COMMENT_HEADER + astToString(ast))
