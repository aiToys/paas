// VitePress 站点配置：中文文档 + 本地全文搜索（中英分词内置）。
// base='/docs/' 子路径部署（core 同源 serve，路由 /docs/*）。
import { defineConfig } from 'vitepress'

export default defineConfig({
  lang: 'zh-CN',
  title: 'OnePaaS 文档',
  description: '一站式 PaaS 平台用户文档——应用、工作负载、DevOps、可观测、MaaS、AI 编排',
  base: '/docs/',
  cleanUrls: false, // 保留 .html 后缀——core 静态 serve 无 cleanUrls 重写，.html 直连最稳

  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/docs/logo.svg' }]],

  themeConfig: {
    nav: [
      { text: '指南', link: '/guide/' },
      { text: '功能', link: '/features/' },
      { text: 'API', link: '/api/' },
      { text: '部署', link: '/deploy/' },
    ],

    sidebar: {
      '/guide/': [
        {
          text: '入门',
          items: [
            { text: '简介', link: '/guide/' },
            { text: '快速开始', link: '/guide/quickstart' },
            { text: '核心概念', link: '/guide/concepts' },
          ],
        },
        {
          text: '账户与安全',
          items: [{ text: '登录与 API Key', link: '/guide/auth' }],
        },
      ],
      '/features/': [
        {
          text: '应用主线',
          items: [
            { text: '应用与工作负载', link: '/features/app' },
            { text: '环境与发布', link: '/features/env' },
            { text: '泳道（灰度隔离）', link: '/features/lane' },
          ],
        },
        {
          text: 'DevOps',
          items: [
            { text: '代码构建与流水线', link: '/features/devops' },
            { text: '变更管理', link: '/features/change' },
          ],
        },
        {
          text: '平台能力',
          items: [
            { text: '数据服务', link: '/features/dataservice' },
            { text: '服务治理', link: '/features/governance' },
            { text: '配置中心', link: '/features/configcenter' },
            { text: '可观测', link: '/features/observability' },
          ],
        },
        {
          text: 'AI 服务',
          items: [
            { text: '模型推理（MaaS）', link: '/features/maas' },
            { text: '智能体编排', link: '/features/ai' },
          ],
        },
      ],
      '/api/': [{ text: 'API 参考', items: [{ text: '概览', link: '/api/' }] }],
      '/deploy/': [
        {
          text: '部署',
          items: [
            { text: '部署方式', link: '/deploy/' },
            { text: 'K8s 集群部署', link: '/deploy/k8s' },
            { text: '离线交付（airsync）', link: '/deploy/airsync' },
          ],
        },
      ],
    },

    // 本地搜索（内置 MiniSearch，中文按字切分可搜）
    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索文档', buttonAriaLabel: '搜索' },
          modal: {
            noResultsText: '没有结果',
            resetButtonTitle: '清除条件',
            footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' },
          },
        },
      },
    },

    outline: { level: [2, 3], label: '本页目录' },
    docFooter: { prev: '上一页', next: '下一页' },
    returnToTopLabel: '回到顶部',
    sidebarMenuLabel: '目录',
    darkModeSwitchLabel: '主题',
    lightModeSwitchTitle: '浅色',
    darkModeSwitchTitle: '深色',
  },
})
