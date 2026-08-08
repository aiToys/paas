-- 更新 builtin 模板 stages：旧 seed 的 deploy.envId 为空，改为占位符自动解析（零操作）。
-- SeedTemplates 幂等（exists 跳过），旧 PG 数据未更新，此 migration 补齐。
UPDATE pipeline_templates SET stages = '[
  {"name":"构建","type":"build"},
  {"name":"部署到开发环境","type":"deploy","params":{"envId":"{{app.env.test}}","imageSource":"priorBuild","strategy":"rolling"}},
  {"name":"冒烟测试","type":"test","params":{"mode":"smoke","path":"/livez"}},
  {"name":"写基线","type":"baseline","params":{"mainBranch":"main","versionStrategy":"auto-increment","mergeMode":"squash"}}
]'::jsonb WHERE id = 'tpl-ci' AND builtin = true;

UPDATE pipeline_templates SET stages = '[
  {"name":"上线审批","type":"approve","params":{"message":"确认发布到生产环境"}},
  {"name":"部署到生产","type":"deploy","params":{"envId":"{{app.env.prod}}","imageSource":"latestReady","strategy":"rolling"}},
  {"name":"写版本","type":"baseline","params":{"mainBranch":"","versionStrategy":"auto-increment","mergeMode":"ff"}}
]'::jsonb WHERE id = 'tpl-cd' AND builtin = true;
