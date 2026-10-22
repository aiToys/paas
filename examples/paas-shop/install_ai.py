#!/usr/bin/env python3
"""创建 PaasShop 只读客服演示：标准 Skill 源文件 → Skill/工具 → Agent 绑定。

同名 Skill 已存在时保留用户编辑。指定演示 Agent 的工具绑定由安装器管理，始终只读；
模型、提示词和知识库保留，skills 追加去重。不会执行推理、退款或部署。
"""
import argparse
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request


class InstallError(RuntimeError):
    """可向操作者展示的安装失败，不包含凭证。"""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, base, token):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise InstallError("平台地址需为不含凭证、查询参数的 http/https 地址")
        self.base, self.token = base.rstrip("/"), token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, method, path, body=None):
        data = json.dumps(body, ensure_ascii=False).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with self.opener.open(req, timeout=20) as resp:
                payload = json.load(resp)
        except urllib.error.HTTPError as exc:
            raise InstallError(f"{method} {path} 被拒绝（HTTP {exc.code}），安装未完成") from None
        except (OSError, ValueError):
            raise InstallError(f"{method} {path} 无法完成或返回非法 JSON，安装未完成") from None
        if not isinstance(payload, dict) or "data" not in payload:
            raise InstallError(f"{method} {path} 不符合平台 data 契约")
        return payload["data"]

    def listing(self, kind):
        items = self.request("GET", "/api/" + kind)
        if items is None:
            return []
        if not isinstance(items, list) or any(not isinstance(x, dict) or not x.get("id") for x in items):
            raise InstallError(f"{kind} 列表格式错误，不能当作空列表继续创建")
        return items


def skill_payload(slug, directory):
    text = (directory / slug / "SKILL.md").read_text(encoding="utf-8")
    if not text.startswith("---\n") or "\n---\n" not in text[4:]:
        raise InstallError(f"Skill 源文件格式错误：{slug}")
    header, instructions = text[4:].split("\n---\n", 1)
    meta = dict(line.strip().split(":", 1) for line in header.splitlines() if ":" in line)
    meta = {k: v.strip() for k, v in meta.items()}
    if meta.get("name") != slug or not instructions.strip():
        raise InstallError(f"Skill 源文件不完整：{slug}")
    return {"name": slug, "description": meta["description"], "instructions": instructions.strip(),
            "category": "service", "enabled": True,
            "useCases": "PaasShop 只读客服，需绑定对应 MCP 方法。",
            "examples": "在 examples/paas-shop/skills/scenarios.json 查看输入和事实检查点；订单号替换为测试环境真实订单。"}


def install(client, mcp_url, model="glm-5.2", kb_id="", agent_name="shop-agent"):
    directory = Path(__file__).parent / "skills"
    payloads = [skill_payload(slug, directory) for slug in ("shop-product-guide", "shop-order-query")]
    # 主列表失败必须阻断，避免权限失败时重复创建。
    existing = {kind: client.listing(kind) for kind in ("skills", "tools", "agents")}

    def ensure(kind, body):
        found = next((x for x in existing[kind] if x.get("name") == body["name"]), None)
        if found:
            if not found.get("enabled", True):
                raise InstallError(f'{kind}/{body["name"]} 已停用，请明确处理后重跑；未自动启用')
            if kind == "tools" and (found.get("type") != "mcp" or found.get("config", {}).get("serverURL") != mcp_url):
                raise InstallError(f'工具 {body["name"]} 已绑定其他服务，保留原配置并停止')
            return found
        created = client.request("POST", "/api/" + kind, body)
        if not isinstance(created, dict) or not created.get("id"):
            raise InstallError(f'{kind}/{body["name"]} 创建结果缺少 ID')
        existing[kind].append(created)
        return created

    skills = [ensure("skills", p) for p in payloads]
    tools = [ensure("tools", {"name": name, "description": desc, "type": "mcp", "enabled": True,
                              "config": {"serverURL": mcp_url}}) for name, desc in (
                                  ("search_products", "查找和比较商品"), ("query_product", "查询商品详情"), ("query_order", "查询订单状态"))]
    agent = next((x for x in existing["agents"] if x.get("name") == agent_name), None)
    if agent:
        agent = client.request("GET", "/api/agents/" + urllib.parse.quote(agent["id"], safe=""))
        if not agent.get("enabled", False):
            raise InstallError("演示 Agent 已停用，保留状态；请明确处理后重跑")
    else:
        agent = {"name": agent_name, "description": "PaasShop Skill 演示客服（商品导购/订单查询）", "model": model,
                 "systemPrompt": "你是 PaasShop 客服。使用已绑定技能和实际工具结果回答，不编造。退款只能引导到人工审批流程。",
                 "maxSteps": 5, "enabled": True, "skills": [], "knowledgeBases": []}
    original = dict(agent)
    agent["skills"] = list(dict.fromkeys((agent.get("skills") or []) + [x["id"] for x in skills]))
    agent["tools"] = [x["id"] for x in tools]  # 演示客服不绑定 refund_order 或其他写工具。
    if kb_id:
        agent["knowledgeBases"] = list(dict.fromkeys((agent.get("knowledgeBases") or []) + [kb_id]))
    if "id" not in agent:
        agent = client.request("POST", "/api/agents", agent)
    elif agent != original:
        agent = client.request("PUT", "/api/agents/" + urllib.parse.quote(agent["id"], safe=""), agent)
    if not isinstance(agent, dict) or not agent.get("id"):
        raise InstallError("Agent 创建或更新结果缺少 ID")
    return {"agentId": agent["id"], "skillIds": [x["id"] for x in skills], "toolIds": [x["id"] for x in tools],
            "enabled": agent.get("enabled", False), "inferenceVerified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default=os.getenv("PAAS_BASE") or os.getenv("PAAS_API"))
    parser.add_argument("--mcp-url", default=os.getenv("PAAS_SHOP_MCP_URL"), help="MCP 服务根地址，平台客户端会追加 /mcp")
    parser.add_argument("--model", default=os.getenv("PAAS_SHOP_MODEL", "glm-5.2"))
    parser.add_argument("--knowledgebase-id", default="")
    args = parser.parse_args()
    if not args.base or not args.mcp_url or not os.getenv("PAAS_TOKEN"):
        parser.error("请设置 PAAS_TOKEN，并提供 --base 和 --mcp-url（或对应环境变量）")
    try:
        print(json.dumps(install(Client(args.base, os.environ["PAAS_TOKEN"]), args.mcp_url, args.model, args.knowledgebase_id), ensure_ascii=False))
    except InstallError as exc:
        print(str(exc) + "；已创建资源保留，修复后可幂等重跑。", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
