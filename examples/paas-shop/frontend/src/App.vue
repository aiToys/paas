<script setup lang="ts">
import { ref, onMounted } from 'vue'

interface Product {
  id: number
  name: string
  price: number
  category: string
  stock: number
  description: string
}

const products = ref<Product[]>([])
const recommends = ref<Product[]>([])
const loading = ref(false)
const error = ref('')

// 客服弹窗
const chatOpen = ref(false)
const chatMessages = ref<{ role: 'user' | 'assistant'; content: string; reasoning?: string }[]>([])
const chatInput = ref('')
const chatLoading = ref(false)
const showReasoning = ref<Record<number, boolean>>({})

async function loadProducts() {
  loading.value = true
  error.value = ''
  try {
    const resp = await fetch('/api/products')
    products.value = await resp.json()
  } catch (e) {
    error.value = '商品加载失败'
  } finally {
    loading.value = false
  }
}

async function loadRecommend() {
  try {
    const resp = await fetch('/api/recommend?userId=user-1')
    const data = await resp.json()
    recommends.value = data.products || []
  } catch {}
}

function openChat() {
  chatOpen.value = true
  if (chatMessages.value.length === 0) {
    chatMessages.value.push({ role: 'assistant', content: '你好！我是 PaasShop 智能客服，可以帮你查商品、了解售后政策。试试问「有什么键盘推荐」或「退货政策」' })
  }
}

async function sendChat() {
  const msg = chatInput.value.trim()
  if (!msg || chatLoading.value) return
  chatInput.value = ''
  chatMessages.value.push({ role: 'user', content: msg })
  const assistantIdx = chatMessages.value.length
  chatMessages.value.push({ role: 'assistant', content: '', reasoning: '' })
  chatLoading.value = true

  try {
    const resp = await fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({ message: msg, userId: 'web-user' }),
    })
    if (!resp.body) throw new Error('no body')
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const line of lines) {
        if (!line.startsWith('data: ')) continue
        const payload = line.slice(6).trim()
        if (payload === '[DONE]') continue
        try {
          const chunk = JSON.parse(payload)
          const delta = chunk.choices?.[0]?.delta
          if (delta?.reasoning_content) {
            chatMessages.value[assistantIdx].reasoning += delta.reasoning_content
          }
          if (delta?.content) {
            chatMessages.value[assistantIdx].content += delta.content
          }
        } catch {}
      }
    }
  } catch (e) {
    chatMessages.value[assistantIdx].content = '客服服务暂不可用，请稍后重试。'
  } finally {
    chatLoading.value = false
  }
}

function toggleReasoning(idx: number) {
  showReasoning.value[idx] = !showReasoning.value[idx]
}

function fmtPrice(p: number) {
  return '¥' + p.toFixed(2)
}

onMounted(() => {
  loadProducts()
  loadRecommend()
})
</script>

<template>
  <div class="app">
    <header class="hdr">
      <div class="logo">🛍️ PaasShop</div>
      <div class="tag">智能商品服务 · 演示微服务 + AI 客服 + 全链路可观测</div>
      <button class="chat-btn" @click="openChat">💬 智能客服</button>
    </header>

    <main class="main">
      <section class="products">
        <h2>全部商品（bff → product → postgres）</h2>
        <div v-if="loading" class="empty">加载中…</div>
        <div v-else-if="error" class="empty err">{{ error }}</div>
        <div v-else class="grid">
          <div v-for="p in products" :key="p.id" class="card">
            <div class="card-cat">{{ p.category }}</div>
            <div class="card-name">{{ p.name }}</div>
            <div class="card-desc">{{ p.description }}</div>
            <div class="card-foot">
              <span class="price">{{ fmtPrice(p.price) }}</span>
              <span class="stock">库存 {{ p.stock }}</span>
            </div>
          </div>
        </div>
      </section>

      <aside class="rec">
        <h2>为你推荐（bff → recommend → redis + product）</h2>
        <div v-if="recommends.length === 0" class="empty">加载中…</div>
        <div v-else class="rec-list">
          <div v-for="p in recommends" :key="p.id" class="rec-item">
            <div class="rec-name">{{ p.name }}</div>
            <div class="rec-price">{{ fmtPrice(p.price) }}</div>
          </div>
        </div>
      </aside>
    </main>

    <!-- 客服弹窗 -->
    <div v-if="chatOpen" class="chat-mask" @click.self="chatOpen = false">
      <div class="chat-win">
        <div class="chat-hdr">
          <span>💬 智能客服</span>
          <button class="chat-close" @click="chatOpen = false">×</button>
        </div>
        <div class="chat-body">
          <div v-for="(m, i) in chatMessages" :key="i" :class="['msg', m.role]">
            <div v-if="m.role === 'assistant' && m.reasoning" class="reasoning">
              <button class="rs-btn" @click="toggleReasoning(i)">
                {{ showReasoning[i] ? '▼' : '▶' }} 思考过程
              </button>
              <div v-if="showReasoning[i]" class="rs-content">{{ m.reasoning }}</div>
            </div>
            <div class="msg-content">{{ m.content || (chatLoading && i === chatMessages.length - 1 ? '思考中…' : '') }}</div>
          </div>
        </div>
        <div class="chat-input">
          <input v-model="chatInput" placeholder="试试：有什么键盘推荐 / 退货政策" @keyup.enter="sendChat" :disabled="chatLoading" />
          <button @click="sendChat" :disabled="chatLoading || !chatInput.trim()">发送</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif; background: #f5f7fa; color: #2c3e50; }
.app { min-height: 100vh; }
.hdr { display: flex; align-items: center; gap: 16px; padding: 14px 32px; background: linear-gradient(135deg, #667eea, #764ba2); color: #fff; box-shadow: 0 2px 8px rgba(0,0,0,.1); }
.logo { font-size: 22px; font-weight: 700; }
.tag { font-size: 13px; opacity: .9; flex: 1; }
.chat-btn { background: rgba(255,255,255,.2); border: 1px solid rgba(255,255,255,.4); color: #fff; padding: 8px 16px; border-radius: 20px; cursor: pointer; font-size: 14px; }
.chat-btn:hover { background: rgba(255,255,255,.3); }
.main { display: grid; grid-template-columns: 1fr 320px; gap: 24px; padding: 24px 32px; }
.products h2, .rec h2 { font-size: 16px; margin-bottom: 16px; color: #5a6a7a; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 16px; }
.card { background: #fff; border-radius: 12px; padding: 16px; box-shadow: 0 1px 3px rgba(0,0,0,.08); transition: transform .2s; }
.card:hover { transform: translateY(-2px); box-shadow: 0 4px 12px rgba(0,0,0,.12); }
.card-cat { display: inline-block; background: #eef; color: #667eea; font-size: 11px; padding: 2px 8px; border-radius: 4px; margin-bottom: 8px; }
.card-name { font-size: 16px; font-weight: 600; margin-bottom: 6px; }
.card-desc { font-size: 13px; color: #8a9bae; margin-bottom: 12px; min-height: 36px; }
.card-foot { display: flex; justify-content: space-between; align-items: center; }
.price { color: #e74c3c; font-weight: 700; font-size: 18px; }
.stock { font-size: 12px; color: #95a5a6; }
.rec { background: #fff; border-radius: 12px; padding: 20px; height: fit-content; box-shadow: 0 1px 3px rgba(0,0,0,.08); }
.rec-list { display: flex; flex-direction: column; gap: 10px; }
.rec-item { display: flex; justify-content: space-between; padding: 10px; background: #f8f9fb; border-radius: 8px; }
.rec-name { font-size: 14px; }
.rec-price { color: #e74c3c; font-weight: 600; }
.empty { color: #95a5a6; font-size: 14px; padding: 20px; }
.err { color: #e74c3c; }
.chat-mask { position: fixed; inset: 0; background: rgba(0,0,0,.3); display: flex; align-items: flex-end; justify-content: flex-end; padding: 24px; z-index: 100; }
.chat-win { background: #fff; width: 400px; max-width: 100%; height: 560px; border-radius: 16px; display: flex; flex-direction: column; box-shadow: 0 8px 32px rgba(0,0,0,.2); }
.chat-hdr { display: flex; justify-content: space-between; align-items: center; padding: 14px 18px; border-bottom: 1px solid #eee; font-weight: 600; }
.chat-close { background: none; border: none; font-size: 24px; cursor: pointer; color: #95a5a6; }
.chat-body { flex: 1; overflow-y: auto; padding: 16px; display: flex; flex-direction: column; gap: 12px; }
.msg { max-width: 85%; }
.msg.user { align-self: flex-end; }
.msg.assistant { align-self: flex-start; }
.msg.user .msg-content { background: #667eea; color: #fff; }
.msg.assistant .msg-content { background: #f0f2f5; color: #2c3e50; }
.msg-content { padding: 10px 14px; border-radius: 12px; font-size: 14px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.reasoning { margin-bottom: 6px; }
.rs-btn { background: none; border: 1px dashed #bbb; color: #888; font-size: 12px; padding: 2px 8px; border-radius: 4px; cursor: pointer; }
.rs-content { margin-top: 4px; padding: 8px; background: #fffaeb; border-left: 3px solid #fbbf24; font-size: 12px; color: #92660a; border-radius: 4px; white-space: pre-wrap; }
.chat-input { display: flex; gap: 8px; padding: 12px; border-top: 1px solid #eee; }
.chat-input input { flex: 1; border: 1px solid #ddd; border-radius: 8px; padding: 8px 12px; font-size: 14px; }
.chat-input button { background: #667eea; color: #fff; border: none; border-radius: 8px; padding: 0 18px; cursor: pointer; font-size: 14px; }
.chat-input button:disabled { opacity: .5; cursor: not-allowed; }
</style>
