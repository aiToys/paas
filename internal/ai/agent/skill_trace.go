package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aitoys/paas/internal/ai/skill"
)

var builtinSkillOrigin = regexp.MustCompile(`^builtin:([a-z0-9-]+)@([A-Za-z0-9.+-]+)#([0-9a-f]{64})$`)

// loadSkillInstructions 记录实际读取的指令快照；正文、描述、工具参数不写入遥测。
// 当前由运行时直接注入上下文，使用平台自定义 span，不冒充 execute_tool/load_skill。
// loaded 仅表示该次读取成功；后续 KB/工具准备、模型请求或回答仍可能失败。
func (r *Runtime) loadSkillInstructions(ctx context.Context, a Agent, id string) (string, error) {
	ctx, span := tracer.Start(ctx, "paas.skill.load", trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("paas.skill.id", id),
			attribute.String("paas.skill.load.mode", "instruction_injection"),
			attribute.String("gen_ai.agent.id", a.ID),
			attribute.String("gen_ai.skill.source.uri", "paas://skills/"+url.PathEscape(id)),
		))
	defer span.End()
	fail := func(kind, message string) (string, error) {
		span.SetAttributes(attribute.String("error.type", kind), attribute.String("paas.skill.load.status", "failed"))
		span.SetStatus(codes.Error, message)
		return "", dependencyErrorf("%s", message)
	}
	if r.skills == nil {
		return fail("skill_store_unavailable", "技能服务未接入")
	}
	sk, err := r.skills.Get(ctx, id)
	if err != nil {
		kind := "skill_store_error"
		if errors.Is(err, skill.ErrSkillNotFound) {
			kind = "skill_not_found"
		}
		return fail(kind, "引用的技能不可用或已禁用，请修复依赖后重试")
	}
	if !sk.Enabled {
		return fail("skill_disabled", "引用的技能不可用或已禁用，请修复依赖后重试")
	}
	hash := sha256.Sum256([]byte(sk.Instructions))
	span.SetAttributes(
		attribute.String("gen_ai.skill.name", sk.Name),
		attribute.String("paas.skill.instructions.sha256", hex.EncodeToString(hash[:])),
		attribute.String("paas.skill.load.status", "loaded"),
	)
	if !sk.UpdatedAt.IsZero() {
		span.SetAttributes(attribute.String("paas.skill.updated_at", sk.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	}
	// 来源模板版本与实际副本内容修订分开，避免把用户编辑后的副本标成原模板内容。
	// 只解析受控来源格式，不将任意旧 installedFrom 文本或带凭证 URI 写入遥测。
	if match := builtinSkillOrigin.FindStringSubmatch(sk.InstalledFrom); match != nil {
		span.SetAttributes(
			attribute.String("paas.skill.template.slug", match[1]),
			attribute.String("paas.skill.template.version", match[2]),
			attribute.String("paas.skill.template.sha256", match[3]),
		)
	}
	return "# 能力：" + sk.Name + "\n" + sk.Instructions, nil
}
