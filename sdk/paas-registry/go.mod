module github.com/aitoys/paas/sdk/paas-registry

go 1.26.0

require github.com/go-zeus/zeus v0.1.0

// 本地开发：zeus 是本地 private module。
// TODO 开源发布：zeus 须先发布为 public module（github.com/go-zeus/zeus vX.Y.Z），
// 或将 zeus 核心（registry/types/app 接口层）vendor 进本仓，移除此 replace。
replace github.com/go-zeus/zeus => /Users/wangtao/data/github.com/go-zeus/zeus
