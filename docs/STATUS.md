# ALIAI — z71701/new-api 状态（本仓权威入口）
<!-- status-sync: state=verified source_commit=66bb4f12b5517896d53304ce7bb8e784ddad5092 updated_at=2026-10-09T12:26:08Z -->

> 本文件是本仓库（backend）唯一现状入口，只记录本仓权威事实。其他仓库的状态以**固定 commit URL** 引用，不声明为动态事实。机器权威快照见 `docs/status.yml`（schema v1.1）。最后更新：2026-10-09。

## 当前阶段
`registration-code-admin` 已通过 PR #14 合入，生命周期 **implemented → verified**。用户已明确授权合并、Tag 和部署；`aliai/v1.0.0-rc.40.5` 已推送到真实合并提交，正在构建不可变镜像。独立 post-merge PR #15 已合入；全部 PR CI 和 Tag 源码验证通过，镜像发布仍待实际 digest。

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
- 功能分支：`codex/registration-code-admin`。
- 实际合并提交：[PR #14 固定提交](https://github.com/z71701/new-api/commit/66bb4f12b5517896d53304ce7bb8e784ddad5092)；Tag `aliai/v1.0.0-rc.40.5` 指向同一提交。
- 基础功能：[PR #12 合并提交](https://github.com/z71701/new-api/commit/1b51fc3e9ac2acb8e6dbb943780ac451d2c81619)。
- 最终 API 交接见 `docs/handoffs/2026-10-03-registration-code-api.yaml`，状态 `implemented / post_merge`，producer_commit 为真实合并 SHA；镜像 digest 待构建证据。
- 验证：受影响 Go 包测试与 vet、注册码专项 race、52 项前端回归、变更文件 lint/格式检查、类型检查与生产构建通过。实际数据库为 SQLite **3.50.4**、MySQL **8.0.40**、PostgreSQL **16.15**；每种均运行新安装及 rc.40.4 数据库升级，完整启动/迁移两次，保留用户/配置及摘要唯一约束。MySQL/PostgreSQL 另含独立日志数据库启动；SQLite 日志路径使用本库。
- 页面实测：管理员无需再次输入密码即可生成；刷新保留列表；普通用户不显示入口，直接访问 `/registration-codes` 返回 403。截图位于 `.github/screenshots/`。

## 注册码管理契约与运行时开关（本仓负责）
- 管理员（role ≥ 10）：`GET /api/registration-codes/settings`、`GET /api/registration-codes/`（分页及名称筛选）、`POST /api/registration-codes/`（名称、数量、有效秒数）。生成不要求重复密码验证，使用既有管理鉴权与速率限制。
- 超级管理员：`PUT /api/option/registration-codes`，`{"enabled":boolean}`，绑定 `registration_code.settings` 操作凭证；通用 option 写接口不能绕过该验证。控制台位置为“身份验证 → 基本身份验证”。
- 侧栏在“兑换码”之后、“订阅”之前；普通用户无法进入页面或调用管理接口。
- `RegistrationCodeEnabled` 数据库 option 是持久化开关；未设置时使用 `REGISTRATION_CODE_ENABLED` 环境变量（默认 false）。公共 status 的 `registration_code_enabled` 反映实际值。
- 启用依赖共享 Redis、至少 32 字符的独立 `REGISTRATION_CODE_API_KEY`；默认 TTL 1800 秒，单批最多 100 个，TTL 上限 86400 秒。
- 管理记录新增 `registration_code_records` 表，保存摘要和 AES-256-GCM 密文；Redis 仍决定有效期与单次使用。失败补偿保留原到期时间，旧消费的补偿不能清除新消费的记录。
- 管理页生成的记录持久化并显示未使用/已使用/已过期。旧码及外部 API 生成记录不回填；轮换独立 API Key 后旧密文无法复制，已有 Redis 有效码仍可使用。
- 保持现有 `POST /api/admin/registration-codes` 独立密钥契约及公共注册输入契约。

## 已合并交付（本仓）
| PR | 内容 | 合并提交 |
|---|---|---|
| #3 | desktop session foundation | 见 main 历史 |
| #5 | desktop auth endpoints | 见 main 历史 |
| #7 | Key 创建幂等与脱敏（idempotency & masking） | 见 main 历史 |
| #8 | desktop E2E acceptance contract | `c1876ec91a814673ec30316da741fb6efa428ebe` |

## 开放项 / Blockers
- PR #14 七项 CI 全部通过，用户已明确授权本次合并、Tag 与部署。
- 本独立 post-merge 更新已固定实际合并 SHA；待镜像构建完成，由发布更新记录 digest，部署仓消费固定 URL 并记录其发布/回滚证据。
- 发版时备份数据库及现有独立密钥，保留共享 Redis。回滚到 rc.40.4 时须核对环境开关，因为该版本尚不消费数据库 `RegistrationCodeEnabled` option。
- Roadmap **PR-D**（发布与统计 Manifest / Dashboard）与已合并 PR #8 的 E2E 契约不同；本任务不推进该 Roadmap。

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
