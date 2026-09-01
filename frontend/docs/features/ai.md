# 智能体编排

五实体 + 运行时：

## 实体

| 实体 | 说明 |
|------|------|
| Agent | 智能体（模型 + 提示词 + 工具 + 技能组合） |
| Tool | 工具（HTTP/MCP 调用，凭证掩码 + SSRF 防护） |
| KnowledgeBase | 知识库（文档上传 → 切片 → 向量化入库） |
| Prompt | 提示词模板 |
| Skill | 可叠加能力指令包 |

## 运行时

- 多轮记忆（conversationStore）
- 评估历史落库（eval 用例 + 运行记录）
- **工作流引擎**：llm / condition / approve / end 节点编排，DB 状态驱动——进程重启自动恢复执行中的 run

## 广场（Marketplace）

- 平台级快照不可变发布；fork 安装到租户
- SanitizeConfig 剔除凭证（快照不带敏感信息）
- 控制台「智能体 → 广场」浏览一键安装
