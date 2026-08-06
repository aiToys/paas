-- 数据服务轻量引擎替换：vector 默认 qdrant（原 milvus 过重需 etcd+minio+pulsar，已弃用）；
-- search 默认 meilisearch（原 elasticsearch/opensearch JVM 重 2G+ heap，已弃用）。
-- 存量 milvus/elasticsearch/opensearch 实例本就 failed（engineImage 返空不拉起），无真实数据，
-- 直接 UPDATE engine 到轻量引擎 + 清空 connection（重启后 reconciler 按新引擎建 Secret/STS）。
-- 注：清空 connection 后应用绑定注入的旧连接信息失效，需重新触发绑定（POST bindings）或重启工作负载。
UPDATE data_services SET spec = jsonb_set(spec, '{engine}', '"qdrant"'), connection = '{}'::jsonb
  WHERE kind = 'vector' AND spec->>'engine' = 'milvus';
UPDATE data_services SET spec = jsonb_set(spec, '{engine}', '"meilisearch"'), connection = '{}'::jsonb
  WHERE kind = 'search' AND spec->>'engine' IN ('elasticsearch', 'opensearch');
