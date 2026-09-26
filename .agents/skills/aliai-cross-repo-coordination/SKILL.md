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

## STATUS lifecycle（v1.1 机器权威）
机器权威文件：`docs/status.yml`；人类视图：`docs/STATUS.md`（顶部 `<!-- status-sync: state=... source_commit=... updated_at=... -->` marker，必须与 status.yml 一致，validator 机器校验）。

### 触发条件（何时必须更 status.yml + STATUS.md）
命中本仓 trigger_paths 的任何 PR 都必须同时更新这两个文件：
`controller/**, service/**, model/**, middleware/**, router/**, docs/openapi/**, docs/*contract*, testdata/**, e2e/**`。
治理自身路径（docs/status.yml、docs/STATUS.md、.github/**、scripts/**、.agents/**）始终被校验，不列入 trigger_paths。只改 status.yml 不改 STATUS.md（或反之）validator 直接 FAIL。

### 必更字段
- `lifecycle.current_state` / `previous_state` / `updated_at` / `source_commit` / `evidence`
- 角色证据门禁（见下）；`tests` 枚举；`blockers` list。

### 更新时点与责任方（三阶段 PR）
1. **feature candidate**（开发者/本 PR 作者）：`current_state=candidate`，`source_commit`=本分支 HEAD，**禁止填 merge SHA**，不产出最终 handoff。
2. **post-merge follow-up**（merge agent，合并后独立 PR）：`current_state=implemented`，`source_commit`=真实 merge SHA；同时把 handoff 升级到 `lifecycle_state=implemented`。
3. **post-build / post-release / post-deploy**（release/deploy agent 或人工）：构建/部署/验收后推进到 `verified` / `released`（backend）/ `accepted`（client）/ `deployed`（deploy），填对应证据字段。

### 状态定义（backend 可用）
planned → candidate → implemented → verified → released；任意态可 → blocked/failed/superseded；blocked/failed → previous_state。

### 合法迁移（validator 拦截非法）
允许：planned→candidate、candidate→implemented、implemented→verified、verified→released（backend）、implemented→accepted（client，需 real_e2e）、implemented→deployed（deploy，需四件套）、任意→blocked/failed/superseded、blocked/failed→previous、deployed→rolled_back。
非法（必须拦截）：candidate→released/accepted/deployed、planned→implemented、跳级、implemented→deployed 无证据。

### 证据门禁
- `released`（backend）：`backend.image_tag` + `backend.image_digest`（`sha256:[a-f0-9]{64}`）必填。
- `verified`：`tests` 需 passed。
- `accepted`（client）：`client.test_level=real_e2e` + `lifecycle.evidence`。
- `deployed`（deploy）：`deploy.actual_digest` + `deploy.current_tag` + `deploy.health_evidence` + `deploy.rollback_to` 四件套全非空。

### 禁止提前声明
- candidate 态禁止填写 merge SHA、禁止出现 `backend.image_digest` / `deploy.actual_digest` 等合并后才有的字段。
- 不得凭记忆伪造 health_evidence / digest；证据必须来自仓库内可验证文件或 CI 记录。
- 不声明其他仓的动态状态，只引用固定 commit URL。

### 人工授权边界
Tag / Release / 部署 / 回滚均需人工明确授权；本 skill 与 validator 不执行这些动作。

## 命名陷阱
GitHub 分支 `pr-d-desktop-e2e-acceptance`（=PR #8，E2E 验收契约，已合并）**≠** Roadmap PR-D（发布与统计 Manifest/Dashboard，未开始）。路由时不得混淆。
