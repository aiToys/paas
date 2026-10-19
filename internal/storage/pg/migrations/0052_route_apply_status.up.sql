-- 路由数据面下发状态（spec 2026-10-17 专项04 §4「配置 → K8s 对象」证据链）：
-- ApplyRepo 装饰器投影后回写。apply_status 空=未下发（Host 空/数据面未启用）；
-- applied=Ingress 已聚合下发；failed=下发失败（apply_error 记脱敏原因）。
ALTER TABLE gov_routes ADD COLUMN IF NOT EXISTS apply_status text NOT NULL DEFAULT '';
ALTER TABLE gov_routes ADD COLUMN IF NOT EXISTS apply_error text NOT NULL DEFAULT '';
ALTER TABLE gov_routes ADD COLUMN IF NOT EXISTS applied_at timestamptz;
