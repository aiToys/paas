#!/bin/bash
# reconcile.sh —— 平台数据对账工具（对账式审计的固化实现，2026-09-04 立项）
#
# 背景：平台无 DB 外键（多租户 + Repository 分层设计），模块间引用全是软外键，
#       级联删除/重建/命名变迁都会产生「孤儿引用/幽灵记录/孤儿实物」。单模块
#       审计永远查不出——只有跨层比对（部署引用 ↔ 平台记录 ↔ 外部实物）才能发现。
#       首案：paas-shop 应用删除重建后工作负载引用旧镜像（孤儿引用），镜像库记录
#       与 registry 实物因 repo 命名变迁（应用名 → app-<ID>）肉眼无法对应。
#
# 用法：./scripts/reconcile.sh            # 全量对账
#       REG=http://<registry> ./scripts/reconcile.sh   # 自定义 registry 地址
#
# 输出分节：[A] 孤儿引用  [B] 幽灵记录  [C] 配额漂移  [D] 重复记录  [E] 规模概览
#           [F] 幽灵 workload（DB 有行无 CR，drift 补投影失效）  [G] 孤儿 CR（CR 有 DB 无行，OrphanGC 失效）
#           [H] dataservice 孤儿/幽灵（CR↔DB 双向，ds OrphanGC 失效探针）
# 退出码：发现需处理的不一致（A1/B1/C1/D1/F1/G1/H1）时非零，可挂 CI。F/G/H 依赖 kubectl，不可达时跳过不算失败。

set -u
REG="${REG:-http://192.168.41.122:30050}"
# 注意：-A（unaligned）默认字段分隔符即 '|'，勿加 -F'|'——变量展开不重新解释引号，
# 引号字符会泄漏进分隔符（踩坑实录）。
PG="kubectl -n paas exec paas-postgres-0 -- env PGPASSWORD=paas psql -U paas -d paas -P pager=off -t -A -c"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# 输出 tee 到结果文件：while-管道子 shell 里置 FAIL 不会传回父 shell（bash 陷阱），
# 退出码改为对结果文件 grep 判定。
OUT="$TMP/out"
exec > >(tee "$OUT") 2>&1

echo "===== [A] 孤儿引用：workload/release 引用了不存在的 Image 记录 ====="
# 引用归一化：image_ref 三种形态——tag（host/repo:tag）、digest 带 host（host/repo@sha256:..）、
# 裸 digest（sha256:..，Release 部署产物）。与 images 表 (full_ref|dig_ref|bare_digest) 三列比对。
$PG "select coalesce(nullif(image_ref,''), image) ref, id from workloads" > "$TMP/wl.txt" 2>/dev/null
$PG "select registry||':'||tag, registry||'@'||digest, digest from images" > "$TMP/img.txt" 2>/dev/null
while IFS='|' read -r ref wid; do
  [ -z "${ref}" ] && continue
  case "${ref}" in
    *@sha256:*|sha256:*) grep -qF "${ref}" "$TMP/img.txt" || { echo "  [A1-孤儿] ${ref}  (${wid})"; FAIL=1; } ;;
    *)                  grep -qxF "${ref}" "$TMP/img.txt" || echo "  [A2-外部镜像] ${ref}  (${wid})（非平台构建产物，如公共基础镜像——预期内）" ;;
  esac
done < "$TMP/wl.txt"
echo "  （A1=需处理：引用已删除应用的镜像；A2=直接引用外部镜像，属正常形态）"

echo "===== [B] 幽灵记录：Image 记录在 registry 无实物 ====="
repos=$(curl -s "$REG/v2/_catalog" | python3 -c "import json,sys; print(' '.join(json.load(sys.stdin).get('repositories') or []))" 2>/dev/null)
> "$TMP/reg.txt"
for r in $repos; do
  curl -s "$REG/v2/$r/tags/list" | python3 -c "import json,sys; d=json.load(sys.stdin); [print('$r:'+t) for t in (d.get('tags') or [])]" >> "$TMP/reg.txt" 2>/dev/null
done
while IFS='|' read -r full_ref _; do
  [ -z "${full_ref}" ] && continue
  repo_tag="${full_ref#*/}"                       # 剥 host:port/ 前缀后与 registry tags 对齐
  grep -qxF "${repo_tag}" "$TMP/reg.txt" || { echo "  [B1-幽灵] ${full_ref}（记录在库、registry 无实物）"; FAIL=1; }
done < "$TMP/img.txt"

echo "===== [C] 配额漂移：billing_usages.counts vs 实际资源数（按租户） ====="
# counts jsonb 各资源 used 计数应 == 对应表按租户实际行数；不等=CheckAndInc/回收配对失衡（泄漏或超收）
for pair in "applications:applications" "workloads:workloads" "data_services:dataservices"; do
  tbl="${pair%%:*}"; key="${pair##*:}"
  $PG "select u.tenant_id, coalesce(u.counts->>'$key','0')::int used, coalesce((select count(*) from $tbl t where t.tenant_id=u.tenant_id),0) actual from billing_usages u" 2>/dev/null |
  while IFS='|' read -r tid used actual; do
    [ "${used}" != "${actual}" ] && echo "  [C1-漂移] ${tid} $key: 计数=${used} vs 实际=${actual}"
  done
done

echo "===== [D] 重复 Image 记录：同 (registry,tag) 多条 ====="
# 语义细分：同 tag 同 digest = 幂等重推（无害，仅提示）；同 tag 不同 digest = digest 不可变被破坏（危险）。
$PG "select registry||':'||tag, count(distinct digest) dd, count(*) n from images group by 1 having count(*)>1" 2>/dev/null | while IFS='|' read -r ref dd n; do
  if [ "${dd}" -gt 1 ]; then
    echo "  [D1-同tag不同digest] ${ref} digest数=${dd} 记录数=${n}（digest 不可变被破坏，需处理）"
  else
    echo "  [D2-幂等重推] ${ref} ×${n}（同 digest 重复构建，无害）"
  fi
done

echo "===== [E] 规模概览 ====="
echo "  registry tags: $(wc -l < "$TMP/reg.txt" | tr -d ' ')  |  images 记录: $(grep -c . "$TMP/img.txt" || echo 0)  |  workload 引用: $(grep -c . "$TMP/wl.txt" || echo 0)"
# registry 侧孤儿实物（registry 有 tag 但 DB 无记录）= 历史级联删除遗留，量级大属预期，按需专项清理。
# 形态对齐：reg.txt 是 repo:tag，img.txt 是 host/repo:tag——剥 host 后比对。
sed 's|^[^/]*/||' "$TMP/img.txt" | cut -d'|' -f1 | sort -u > "$TMP/img_short.txt"
grep -vxF -f "$TMP/img_short.txt" "$TMP/reg.txt" | head -5 | while IFS= read -r rt; do
  echo "  [E1-reg孤儿实物] ${rt}（registry 有、DB 无——历史遗留）"
done
echo "  （E1 仅列前 5 条；历史级联删除遗留，不阻塞）"

# K8s 实态对账基线：Workload CR 归一化为 tenant|id（ns 剥 paas- 前缀 = tenant_id，CR 名 = workload ID）。
# 集群不可达时 F/G 节跳过（输出提示，不算失败——本节依赖 kubectl 连通性，独立于 PG 侧各节）。
if kubectl get workloads.core.aitoys.github.com -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"\n"}{end}' > "$TMP/cr.txt" 2>/dev/null; then
  sed 's/^paas-//' "$TMP/cr.txt" | grep . | sort > "$TMP/cr_norm.txt"
  $PG "select tenant_id, id from workloads order by tenant_id, id" 2>/dev/null | grep . | sort > "$TMP/wl_all.txt"

  echo "===== [F] 幽灵 workload：DB 有行但 K8s 无 Workload CR ====="
  # 语义：启动 drift 修复（reconcileWorkloadDrift 补投影）失效或未覆盖——DB 行存在但集群无期望状态。
  comm -23 "$TMP/wl_all.txt" "$TMP/cr_norm.txt" | while IFS='|' read -r tid wid; do
    echo "  [F1-幽灵workload] ${tid}/${wid}（DB 有、CR 无——drift 补投影未生效，集群实际不运行）"
  done

  echo "===== [G] 孤儿 CR：K8s 有 Workload CR 但 DB 无行 ====="
  # 语义：OrphanGC（周期对账回收平台侧已删的 CR，宽限 10min）失效——僵尸 CR 会抢占 Service 名（chat 503 元凶形态）。
  comm -13 "$TMP/wl_all.txt" "$TMP/cr_norm.txt" | while IFS='|' read -r tid wid; do
    echo "  [G1-孤儿CR] ${tid}/${wid}（CR 有、DB 无——OrphanGC 未回收，僵尸资源）"
  done

  # 数据服务同款对账（2026-09-25 立项：dataservice 曾无 OrphanGC，一次对账实测 9 个孤儿
  # CR+STS 白跑 Pod 并污染监控数据；平台侧已补 OrphanGC，本节为其失效探针）。
  if kubectl get dataservices.core.aitoys.github.com -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"\n"}{end}' > "$TMP/ds_cr.txt" 2>/dev/null; then
    sed 's/^paas-//' "$TMP/ds_cr.txt" | grep . | sort > "$TMP/ds_cr_norm.txt"
    $PG "select tenant_id, id from data_services order by tenant_id, id" 2>/dev/null | grep . | sort > "$TMP/ds_all.txt"
    echo "===== [H] 孤儿 dataservice：K8s 有 DataService CR 但 DB 无行 ====="
    # CR 名即平台数据服务 ID（ds-<ts>-<n>），双向对账：H1=孤儿 CR（OrphanGC 失效）；H2=幽灵（drift 类，暂无补投影机制，提示性）。
    comm -13 "$TMP/ds_all.txt" "$TMP/ds_cr_norm.txt" | while IFS='|' read -r tid did; do
      echo "  [H1-孤儿ds] ${tid}/${did}（CR 有、DB 无——ds OrphanGC 未回收，STS 白耗资源）"
    done
    comm -23 "$TMP/ds_all.txt" "$TMP/ds_cr_norm.txt" | while IFS='|' read -r tid did; do
      echo "  [H2-幽灵ds] ${tid}/${did}（DB 有、CR 无——期望状态未投影）"
    done
  fi
else
  echo "===== [F]/[G]/[H] K8s 实态对账：跳过（kubectl 不可达） ====="
fi

# 退出码：A1/B1/C1/D1/F1/G1/H1 任一存在即非零（子 shell FAIL 不传递，改 grep 结果文件）。
grep -qE "\[(A1|B1|C1|D1|F1|G1|H1)-" "$OUT" && { echo; echo "==> 对账不通过（存在 A1/B1/C1/D1/F1/G1/H1）"; exit 1; }
echo; echo "==> 对账通过"
exit 0
