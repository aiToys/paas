-- AI 租户表 RLS 补齐（第 10 轮深度审计 I2）：
-- 0011-0015/0031/0033 建的 AI 表当年未跟 0032/0035 的 RLS 惯例；0040 用错 GUC 名（paas.tenant 不存在）。
-- 统一补 ENABLE + 同款策略（app.tenant_id + IS NULL 放行 owner 连接）。幂等。

-- 修正 0040 的错误 GUC
ALTER TABLE ai_workflows ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ai_workflows;
CREATE POLICY tenant_isolation ON ai_workflows
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_workflow_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ai_workflow_runs;
CREATE POLICY tenant_isolation ON ai_workflow_runs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

-- 其余 AI 租户表统一补（表名前缀策略命名，与 0035 lanes_tenant_isolation 同款）
ALTER TABLE ai_knowledgebases ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_knowledgebases_tenant_isolation ON ai_knowledgebases;
CREATE POLICY ai_knowledgebases_tenant_isolation ON ai_knowledgebases
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_documents ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_documents_tenant_isolation ON ai_documents;
CREATE POLICY ai_documents_tenant_isolation ON ai_documents
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_chunks ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_chunks_tenant_isolation ON ai_chunks;
CREATE POLICY ai_chunks_tenant_isolation ON ai_chunks
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_tools ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_tools_tenant_isolation ON ai_tools;
CREATE POLICY ai_tools_tenant_isolation ON ai_tools
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_prompts ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_prompts_tenant_isolation ON ai_prompts;
CREATE POLICY ai_prompts_tenant_isolation ON ai_prompts
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_agents ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_agents_tenant_isolation ON ai_agents;
CREATE POLICY ai_agents_tenant_isolation ON ai_agents
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_eval_cases ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_eval_cases_tenant_isolation ON ai_eval_cases;
CREATE POLICY ai_eval_cases_tenant_isolation ON ai_eval_cases
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE eval_runs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS eval_runs_tenant_isolation ON eval_runs;
CREATE POLICY eval_runs_tenant_isolation ON eval_runs
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE ai_skills ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_skills_tenant_isolation ON ai_skills;
CREATE POLICY ai_skills_tenant_isolation ON ai_skills
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
