// vitest alias 目标：FormDrawer 等组件按需引入的 EP 样式 side-effect import 在
// jsdom 中无意义且 node ESM 加载不了 node_modules 内 css，统一指向本空模块。
export {}
