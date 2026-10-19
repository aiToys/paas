export function playgroundMode(model: { id: string; capabilities?: string[] } | undefined): 'chat' | 'embedding' | 'unsupported' {
  if (!model) return 'unsupported'
  if (model.id.startsWith('agent:') || model.capabilities?.includes('chat')) return 'chat'
  return model.capabilities?.includes('embedding') ? 'embedding' : 'unsupported'
}

export function conversationHistory(messages: { role: string; content: string; error?: string; stopped?: boolean; interrupted?: boolean }[]) {
  const complete = (m: typeof messages[number] | undefined) => m && m.content.trim() && !m.error && !m.stopped && !m.interrupted
  return messages.filter((m, i) => complete(m) && (m.role !== 'user' || (messages[i + 1]?.role === 'assistant' && complete(messages[i + 1]))))
    .map(m => ({ role: m.role, content: m.content }))
}

// 一个完整 SSE 事件到齐后才解析；错误帧与损坏 JSON 不得当作正常结束。
export function chatEvent(frame: string): { done?: true; delta?: { content?: string; reasoning_content?: string } } {
  const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n').trim()
  if (!data) return {}
  if (data === '[DONE]') return { done: true }
  let value: { error?: string | { message?: string }; choices?: { delta?: { content?: string; reasoning_content?: string } }[] }
  try { value = JSON.parse(data) } catch { throw new Error('流式响应格式错误，回答未完成') }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('流式响应格式错误，回答未完成')
  if (value.error) throw new Error(typeof value.error === 'string' ? value.error : value.error.message || '模型流式请求失败')
  return { delta: value.choices?.[0]?.delta }
}
