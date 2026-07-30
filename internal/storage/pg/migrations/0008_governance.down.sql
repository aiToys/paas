-- 反向 DROP：与 up 创建顺序相反，先删依赖方（routes/breakers/instances）再删 services。
DROP TABLE IF EXISTS gov_breakers CASCADE;
DROP TABLE IF EXISTS gov_routes CASCADE;
DROP TABLE IF EXISTS gov_instances CASCADE;
DROP TABLE IF EXISTS gov_services CASCADE;
