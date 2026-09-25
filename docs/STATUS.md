# ALIAI — z71701/new-api 状态（本仓权威入口）
<!-- status-sync: state=released source_commit=598e708adeb15c6f5ad8dda24f448ae0a024238a updated_at=2026-09-26T00:00:00Z -->

> 本文件是本仓库（backend）唯一现状入口，只记录本仓权威事实。其他仓库的状态以**固定 commit URL** 引用，不声明为动态事实。机器权威快照见 `docs/status.yml`（schema v1.1）。最后更新：2026-09-26。

## 当前阶段
生命周期状态（`docs/status.yml`）：**released**（上一状态 verified）。桌面端（Electron）认证链路已合并并进入联调基线；后端为 API/OpenAPI/错误码的权威来源。镜像 `aliai/v1.0.0-rc.40.3` 已构建，digest 已固定记录。跨仓治理（governance manifest、status.yml、handoff schema、validator）升级到 v1.1（status lifecycle 机器权威）。

## 本仓权威事实
<!-- backend = API/OpenAPI/错误码/runtime switches/fixtures/后端状态 -->
- 角色：`backend`，权威字段 = API / OpenAPI / 错误码 / fixtures / 后端发布状态。
- 桌面端认证路由（共享 JSON 信封）：
  - `POST /api/desktop/auth/login`
  - `POST /api/desktop/auth/verify`
  - `POST /api/desktop/auth/refresh`
  - `POST /api/desktop/auth/logout`
- 路由中间件链：`DesktopAuthRateLimit` + `DisableCache` + `anonymousRequestBodyLimit`。
- 契约文件（随提交固定）：`docs/desktop-auth-openapi.yaml`（OpenAPI 3.1.0）、`docs/desktop-auth-acceptance-checklist.md`、`docs/desktop-auth-owasp-controls.md`、`docs/desktop-turnstile-integration.md`。

## 版本与提交
- 分支 / HEAD：`main` @ `598e708adeb15c6f5ad8dda24f448ae0a024238a`（`docs: add cross-repo governance & backend STATUS (#9)`，fetch 后确认）。
- 生命周期：`released`（source_commit = 上列 HEAD，见 `docs/status.yml` 的 `lifecycle.source_commit`）。
- 最新镜像：`aliai/v1.0.0-rc.40.3`，digest `sha256:ae5b2f699558611bea6ea50a5ac1865b79e133d1ebf7877f9e9a6cf869118593`（本仓 released 证据门禁字段，见 `docs/status.yml` 的 `backend.*`）。digest 的生产部署落地由 `z71701/aliai-deploy` 记录（见跨仓引用）。

## Runtime / 部署开关（仅本仓负责）
| 开关 | 值 | 说明 |
|---|---|---|
| `password_login_enabled` | `true` | 密码登录总开关 |
| `password_login_encryption_enabled` | `false` | 密码登录加密开关（当前关闭） |
| `turnstile_check` | `false` | Turnstile 人机校验（当前关闭） |

## 已合并交付（本仓）
| PR | 内容 | 合并提交 |
|---|---|---|
| #3 | desktop session foundation | 见 main 历史 |
| #5 | desktop auth endpoints | 见 main 历史 |
| #7 | Key 创建幂等与脱敏（idempotency & masking） | 见 main 历史 |
| #8 | desktop E2E acceptance contract | `c1876ec91a814673ec30316da741fb6efa428ebe` |

## 开放项 / Blockers
- Roadmap **PR-D**（发布与统计 Manifest / Dashboard）**未开始**。
- 注意：Roadmap PR-D 与已合并的分支 `pr-d-desktop-e2e-acceptance`（PR #8）**不是同一回事**，见“命名陷阱”。

## 跨仓引用（固定 URL+commit，非动态状态）
> 下列 SHA 为 2026-09-25 治理引导时观察到的下游快照，仅作固定引用基线；不代表这些仓的当前动态状态。
| 关联仓 | 引用 | 固定 commit |
|---|---|---|
| Yohalloo/aliai-desktop | https://github.com/Yohalloo/aliai-desktop/commit/2d0318ab172845c4aa5a0f9a09a1d59b8ad4d60d | `2d0318ab172845c4aa5a0f9a09a1d59b8ad4d60d` |
| z71701/aliai-deploy | https://github.com/z71701/aliai-deploy/commit/7a4ef6e432350280b4da61de3922a6765b94206c | `7a4ef6e432350280b4da61de3922a6765b94206c` |

## 命名陷阱
- GitHub 分支 `pr-d-desktop-e2e-acceptance`（=PR #8，E2E 验收契约，已合并）**≠** Roadmap PR-D（发布与统计 Manifest/Dashboard，未开始）。两者命名相近，禁止混淆。

## Handoff 记录
见 [docs/handoffs/README.md](handoffs/README.md)。
