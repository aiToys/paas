<script setup lang="ts">
// 能力矩阵：六大能力域，内容对齐项目真实落地状态（非"规划中"）。
// 每卡含已落地子能力清单，让访客快速判断覆盖度。
const groups = [
  {
    icon: '◈',
    title: 'MaaS 推理平台',
    tagline: '轻资产 · 聚合第三方供应商',
    items: [
      '聚合 OpenAI / DeepSeek / 通义千问（OpenAI 兼容协议）',
      'Model → Channel → Provider 三层抽象',
      '请求级 failover + 流式透传（含 reasoning）+ Token 计量',
      '平台级凭证托管 · 配置主密钥后加密持久化 · API 掩码展示',
    ],
  },
  {
    icon: '◆',
    title: '服务治理',
    tagline: '注册发现 · 路由 · 配置管理',
    items: [
      '服务注册发现（K8s Endpoints 真源）',
      '配置中心：版本 / 发布 / 回滚 / 泳道覆盖 / 客户端发现',
      'API 网关路由（Host 聚合 Ingress 下发）',
      '熔断规则与模拟评估（真实流量驱动待接入）',
    ],
  },
  {
    icon: '▲',
    title: 'DevOps CI/CD',
    tagline: '代码到上线全链路',
    items: [
      '代码 → 构建（K8s Job DooD）→ 镜像（digest 不可变）',
      '交付预演 · 产物锁定 · 审批决策 · 泳道验证 · 发布回滚',
      '变更批次协作 · webhook 投递追踪与重放',
      '内置 Gitea 代码托管 · PR 评审 · webhook 自动触发',
    ],
  },
  {
    icon: '■',
    title: '资源中心',
    tagline: '一套领域覆盖六类数据服务',
    items: [
      'DB / 缓存 / 消息队列 / 对象存储 / 向量库 / 搜索',
      '三模式：托管 / 外部共享 / 外部独占',
      'K8s StatefulSet 数据面纳管 + exporter 监控',
      '应用 Add-on 绑定 · 连接信息自动注入',
    ],
  },
  {
    icon: '◉',
    title: '应用工作台',
    tagline: '应用主线的完整旅程',
    items: [
      '应用 × 环境双维主线，prod / test 阶序晋升',
      '全链路泳道灰度：流量染色 · 降级基线 · TTL 自动回收',
      'Service / Job / CronJob · 实例日志与事件 · 重启与重建',
      '应用成员权限（owner / maintainer / developer / viewer）',
    ],
  },
  {
    icon: '✦',
    title: 'AI 编排',
    tagline: '智能体构建 · 运行 · 共享',
    items: [
      '智能体 / 工具 / 知识库 / 提示词 / 技能五实体',
      '工作流引擎（LLM / 条件 / 审批节点）· DB 状态驱动恢复',
      '多轮记忆与评估记录 · 可接 AgentRun 智能研发执行域',
      '智能体市场：快照 fork 安装，凭证自动剔除',
    ],
  },
]

// 平台横切能力（非能力域，所有切片继承）
const cross = [
  { k: '多租户 RBAC', v: 'API Key 三元组凭证 · Repository 强制租户过滤 · 跨租户不泄漏' },
  { k: '生产安全防护', v: 'prod:write 权限 · gated 15min 超时 · 视觉强隔离 · 危险操作确认' },
  { k: '可观测', v: 'OTel + Prometheus + Loki + Jaeger；trace/log 关联 + 告警状态机' },
  { k: '安全 · 计费', v: 'AES-256-GCM 静态加密 + 只增审计；租户配额 + 用量 + 账单' },
  { k: 'PostgreSQL', v: '业务数据持久化 · 查询层租户隔离；未配置数据库时使用内存模式' },
  { k: 'OpenAPI 契约', v: 'route registry 单一真源 · 前端 openapi-typescript 生成' },
]
</script>

<template>
  <section id="capabilities" class="section">
    <div class="container">
      <div class="section-head center">
        <span class="eyebrow">能力矩阵</span>
        <h2 class="section-title">六大能力域，一个控制面</h2>
        <p class="section-sub">
          围绕应用与环境组织资源，共用身份、权限与审计。真实构建、监控、模型推理等能力需配置对应后端。
        </p>
      </div>

      <div class="grid">
        <article v-for="g in groups" :key="g.title" class="card">
          <div class="card-head">
            <span class="icon" :class="g.title[0]">{{ g.icon }}</span>
            <div>
              <h3>{{ g.title }}</h3>
              <span class="tagline">{{ g.tagline }}</span>
            </div>
          </div>
          <ul>
            <li v-for="it in g.items" :key="it">{{ it }}</li>
          </ul>
        </article>
      </div>

      <div class="cross">
        <div class="cross-head">
          <span class="eyebrow">平台横切</span>
          <h3>所有切片自动继承的平台能力</h3>
        </div>
        <div class="cross-grid">
          <div v-for="c in cross" :key="c.k" class="cross-item">
            <div class="cross-k">{{ c.k }}</div>
            <div class="cross-v">{{ c.v }}</div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 18px;
}
.card {
  padding: 26px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s, transform 0.15s;
}
.card:hover {
  border-color: var(--border-strong);
  transform: translateY(-2px);
}
.card-head {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 18px;
}
.icon {
  width: 42px;
  height: 42px;
  display: grid;
  place-items: center;
  border-radius: 11px;
  background: var(--brand-soft);
  color: var(--brand-2);
  font-size: 20px;
  flex-shrink: 0;
}
.card h3 {
  font-size: 17px;
  font-weight: 700;
}
.tagline {
  font-size: 12.5px;
  color: var(--text-faint);
}
ul {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
li {
  position: relative;
  padding-left: 22px;
  font-size: 14px;
  color: var(--text-dim);
  line-height: 1.55;
}
li::before {
  content: '';
  position: absolute;
  left: 0;
  top: 8px;
  width: 12px;
  height: 6px;
  border-left: 2px solid var(--accent);
  border-bottom: 2px solid var(--accent);
  transform: rotate(-45deg);
  opacity: 0.7;
}

.cross {
  margin-top: 40px;
  padding: 32px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.cross-head {
  margin-bottom: 22px;
}
.cross-head h3 {
  font-size: 18px;
  margin-top: 8px;
}
.cross-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 18px 28px;
}
.cross-k {
  font-size: 13.5px;
  font-weight: 700;
  color: var(--text);
  margin-bottom: 4px;
}
.cross-v {
  font-size: 13px;
  color: var(--text-faint);
  line-height: 1.5;
}

@media (max-width: 880px) {
  .grid {
    grid-template-columns: 1fr;
  }
  .cross-grid {
    grid-template-columns: 1fr 1fr;
  }
}
@media (max-width: 560px) {
  .cross-grid {
    grid-template-columns: 1fr;
  }
}
</style>
