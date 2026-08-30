/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  // eslint-disable-next-line @typescript-eslint/no-explicit-any -- vue 模块声明的标准写法
  const component: DefineComponent<Record<string, never>, Record<string, never>, any>
  export default component
}
