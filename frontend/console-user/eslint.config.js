// ESLint 9 flat config —— console-user 基础纪律
// 参考 console-admin/eslint.config.js 裁剪：无四层架构目录边界（那是 vue-admin 基座的），
// 保留 vue 推荐规则 + TS 严格纪律（no-explicit-any / no-unused-vars）。
import js from '@eslint/js'
import vue from 'eslint-plugin-vue'
import tseslint from '@typescript-eslint/eslint-plugin'
import tsparser from '@typescript-eslint/parser'
import globals from 'globals'

export default [
  {
    ignores: [
      'dist/**',
      'node_modules/**',
      'coverage/**',
      'src/api/types.gen.ts',
      // 防闪烁脚本运行在 <head> 内联，需最小化且无模块体系
      'public/theme-init.js'
    ]
  },
  js.configs.recommended,
  ...vue.configs['flat/recommended'],
  {
    files: ['**/*.{ts,tsx,vue}'],
    languageOptions: {
      // .vue 走 vue-eslint-parser，<script> 块喂 tsparser；.ts 直接用 tsparser
      parserOptions: {
        ecmaVersion: 'latest',
        sourceType: 'module',
        parser: tsparser
      }
    },
    plugins: {
      '@typescript-eslint': tseslint
    },
    rules: {
      ...tseslint.configs.recommended.rules,
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }
      ],
      // TS 项目由 tsc 检查未定义变量；原生 no-undef 不识别 TS 类型注解
      'no-undef': 'off',
      // 存量代码未按 prettier 风格排版，纯格式规则关闭（聚焦实质纪律）；
      // 新代码建议遵循单行单属性，但不作强制
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-self-closing': 'off',
      'vue/attributes-order': 'off',
      'vue/html-indent': 'off',
      'vue/first-attribute-newline': 'off'
    }
  },
  // .ts/.tsx 文件没有 vue-eslint-parser 包装，必须显式设置顶层 parser
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      parser: tsparser
    }
  },
  // 根目录配置文件运行在 Node 环境
  {
    files: ['*.config.ts', '*.config.js', 'vite.config.ts'],
    languageOptions: {
      globals: { ...globals.node }
    }
  },
  // src/ 运行在浏览器环境
  {
    files: ['src/**/*.{ts,tsx,vue}'],
    languageOptions: {
      globals: { ...globals.browser }
    }
  }
]
