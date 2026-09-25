# Handoffs（跨仓交接物）

本目录存放本仓作为**生产者**发出的跨仓交接 YAML。下游通过固定 URL+commit 消费，不需要克隆本仓。

## Schema（v1.0）

每份 handoff 是一个 YAML 文件，命名 `YYYY-MM-DD-<slug>.yaml`，必须包含以下字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `schema_version` | string | 固定 `"1.0"` |
| `producer_repo` | string | `owner/repo` |
| `producer_commit` | string | 40 位完整 SHA，本 handoff 描述的提交 |
| `artifact` | string | 交接物名称，如 `desktop-auth-api` |
| `version` | string | 版本或 tag |
| `change_type` | enum | `feature` / `bugfix` / `contract_change` / `deprecation` / `release` / `docs` / `infra` |
| `compatibility` | enum | `none` / `backward` / `breaking` / `conditional` |
| `summary` | string | 一句话摘要 |
| `created_at` | string | `YYYY-MM-DD` |
| `contract_refs` | list | `{url, kind}`，必须是带 commit 的固定 URL |
| `tests` | list | `{name, status, evidence_url}`，status ∈ passed/failed/blocked/not_run |
| `runtime_switches` | map | 与本交接物相关的运行时开关键值 |
| `downstream_actions` | list | `{repo, action, required}` |
| `blockers` | list | 字符串列表，无则空数组 |

模板见 [TEMPLATE.yaml](TEMPLATE.yaml)。

## 规则
1. 只记录本仓产出的交接物；不记录其他仓的动态状态。
2. 所有 URL 必须含 commit SHA，不使用 `main` 分支浮动引用。
3. 校验：`python scripts/aliai_validate.py <repo-root>`。
4. 命名陷阱：`pr-d-desktop-e2e-acceptance`（PR #8 E2E 契约）≠ Roadmap PR-D（Manifest/Dashboard）。

> 协作者三仓治理说明见 [Yohalloo/aliai-desktop PR #3](https://github.com/Yohalloo/aliai-desktop/pull/3)（合并后更新为 main 固定 commit URL）。
