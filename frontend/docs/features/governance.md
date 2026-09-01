# 服务治理

四件套：

## 服务与实例

- 服务注册表 + **真源 = K8s Endpoints**（`InstanceDiscoverer` 自动发现），手动注册兜底
- 治理 Service 名与 K8s Service 名对齐（数据面发现靠此约定）

## 路由（Route）

- Host 对外域名 + Path 规则（Kong 式域名匹配）
- ApplyRepo → K8sRouteApplier 按 host 聚合多 path 下发 Ingress

## 熔断（CircuitBreaker）

即时评估确定性生成；真实流量采集驱动留后续版本。

## 与配置中心联动

Namespace 关联 Service 前端聚合双向显示（避免跨模块后端耦合）。
