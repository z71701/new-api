---
name: aliai-cross-repo-coordination
description: "ALIAI 后端仓（new-api）跨仓协调：本仓生产 API/OpenAPI/错误码/fixtures 与 backend handoff。当后端接口、契约、运行时开关变更，或需要向下游 aliai-desktop/aliai-deploy 发出交接物时使用。不自动合并/打 Tag/部署。"
---

# ALIAI 跨仓协调 — 后端生产者（new-api）

## 本仓角色与权威字段
- 角色：backend（最上游生产者）
- 本仓权威：API、OpenAPI、错误码、fixtures、运行时开关、后端 handoff
- 下游消费者：Yohalloo/aliai-desktop（客户端）、z71701/aliai-deploy（部署）

## 必须先读（本仓文件）
1. `.github/aliai-governance.yml` — 治理清单、权威字段、上下游
2. `docs/STATUS.md` — 本仓当前状态（唯一权威，不复制其他仓动态状态）
3. `docs/handoffs/README.md` — handoff schema v1.0 与模板
4. `scripts/aliai_validate.py` — 校验器入口：`python scripts/aliai_validate.py .`

## 本仓路由
1. API/OpenAPI/错误码/fixtures/运行时开关变更后，先更新 `docs/STATUS.md` 的本仓权威字段。
2. 生成交接物 `docs/handoffs/<YYYY-MM-DD>-<slug>.yaml`（schema v1.0）：
   - `producer_commit` 钉当前合并提交的 40 位 SHA
   - `contract_refs` 必须是 `https://github.com/z71701/new-api/blob/<40sha>/<path>` 固定 URL
   - `runtime_switches` 记录与本交接物相关的开关值
   - `downstream_actions` 列出 aliai-desktop / aliai-deploy 需要做什么
3. 下游通过 handoff 的固定 URL 消费；**本仓不写入下游仓**，不为下游创建 PR（由 merge/deploy agent 或人工触发）。
4. 运行 `python scripts/aliai_validate.py .` 与 `--self-test`，确认 handoff 合法。

## 跨仓读取规则
- 其他仓状态只通过**线上固定 commit URL** 读取（`gh api repos/{owner}/{repo}/contents/{path}?ref=<sha>`），不假设本地克隆其他仓。
- 不复制其他仓的动态状态或完整契约到本仓；本仓只记录固定 commit 引用。

## 人工授权边界
- 不自动合并 PR、不打 Tag、不触发部署——均需人工明确授权。
- 不保存密钥、固定账号、生产密码；认证用 `gh auth` 或进程级 `GH_TOKEN`。

## 命名陷阱
GitHub 分支 `pr-d-desktop-e2e-acceptance`（=PR #8，E2E 验收契约，已合并）**≠** Roadmap PR-D（发布与统计 Manifest/Dashboard，未开始）。路由时不得混淆。
