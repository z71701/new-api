# ALIAI — z71701/new-api 状态（本仓权威入口）
<!-- status-sync: state=implemented source_commit=1b51fc3e9ac2acb8e6dbb943780ac451d2c81619 updated_at=2026-10-08T11:05:20Z -->

> 本文件是本仓库（backend）唯一现状入口，只记录本仓权威事实。其他仓库的状态以**固定 commit URL** 引用，不声明为动态事实。机器权威快照见 `docs/status.yml`（schema v1.1）。最后更新：2026-10-08。

## 当前阶段
生命周期状态（`docs/status.yml`）：**implemented**（注册码功能上一状态 candidate，见原候选 handoff）。PR #12 已合并，当前快照开始跟踪注册码 API 的合并后交付；真实合并提交为 `1b51fc3e9ac2acb8e6dbb943780ac451d2c81619`。Tag `aliai/v1.0.0-rc.40.4` 已指向该提交，[GHCR 镜像构建](https://github.com/z71701/new-api/actions/runs/37767539410)尚未完成。

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
- 分支 / 合并提交：`main` @ `1b51fc3e9ac2acb8e6dbb943780ac451d2c81619`（`feat: add one-time registration codes (#12)`，squash 合并）。
- 2026-10-08 修复：Redis 服务端绝对到期时间在失败恢复后保持不变；取消请求后的补偿使用独立 3 秒超时。专项测试先在旧实现复现失败，再在修复后通过；受影响包测试、go vet、专项 race 及真实 Redis 6.2.24 Lua 验证通过。
- 生命周期：`implemented`（source_commit = 上列真实合并提交，handoff 的 `producer_commit` 与契约引用使用同一 SHA）。
- 新版本 tag：`aliai/v1.0.0-rc.40.4`，annotated tag；远端解引用确认指向上列合并提交。
- 上一已发布镜像基线：`aliai/v1.0.0-rc.40.3`，digest `sha256:ae5b2f699558611bea6ea50a5ac1865b79e133d1ebf7877f9e9a6cf869118593`；`backend.*` 暂保留该历史基线，新的镜像 digest 待构建证据产生后记录。
- [合并前 CI](https://github.com/z71701/new-api/actions/runs/37721680678)与[治理校验](https://github.com/z71701/new-api/actions/runs/37721680656)通过，覆盖后端 vet/build/test、独立 relaykit 构建、前端 typecheck/test 以及 MySQL 5.7/8.0、PostgreSQL 9.6/16。

## Runtime / 部署开关（仅本仓负责）
| 开关 | 值 | 说明 |
|---|---|---|
| `password_login_enabled` | `true` | 密码登录总开关 |
| `password_login_encryption_enabled` | `false` | 密码登录加密开关（当前关闭） |
| `turnstile_check` | `false` | Turnstile 人机校验（当前关闭） |
| `registration_code_enabled` | `false` | 注册码功能默认关闭；启用时要求共享 Redis 与独立 API Key |
| `registration_code_ttl_seconds` | `1800` | 默认注册码有效期 30 分钟 |

## 已合并交付（本仓）
| PR | 内容 | 合并提交 |
|---|---|---|
| #3 | desktop session foundation | 见 main 历史 |
| #5 | desktop auth endpoints | 见 main 历史 |
| #7 | Key 创建幂等与脱敏（idempotency & masking） | 见 main 历史 |
| #8 | desktop E2E acceptance contract | `c1876ec91a814673ec30316da741fb6efa428ebe` |
| #12 | 一次性注册码与到期/取消补偿 | `1b51fc3e9ac2acb8e6dbb943780ac451d2c81619` |

## 开放项 / Blockers
- 注册码已合并；新镜像发布等待上述构建结果，部署由下游仓另行记录。
- QQ Bot 需要在后续消费者仓或独立 Bot 项目中调用 `POST /api/admin/registration-codes`。
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
