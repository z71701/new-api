# 桌面端认证联调准入交付单（Desktop Auth Acceptance Checklist）

> 本文档是桌面端（Electron）认证链路与后端联调的准入交付单。所有环境相关的秘密值、部署地址均以 `<deployment-specific...>` 占位符给出，**严禁**将真实 secret、密钥、内部地址提交入库。联调开始前由部署负责人逐项填写第 1～3 章，测试负责人按第 4 章执行用例并回填结果。

- 适用范围：`POST /api/desktop/auth/login`、`POST /api/desktop/auth/verify`、`POST /api/desktop/auth/refresh`、`POST /api/desktop/auth/logout`。
- 协议规范：见同目录 [desktop-auth-openapi.yaml](./desktop-auth-openapi.yaml)（OpenAPI 3.1.0）。
- 安全控制基线：见 [desktop-auth-owasp-controls.md](./desktop-auth-owasp-controls.md)。
- Turnstile 集成规范：见 [desktop-turnstile-integration.md](./desktop-turnstile-integration.md)。

---

## 1. 环境信息

| 项 | 值 |
| --- | --- |
| Base URL | `<deployment-specific base URL, e.g. https://api.example.com>` |
| OpenAPI 规范 URL | `<base URL>/docs/desktop-auth-openapi.yaml`（联调环境）或本地 `docs/desktop-auth-openapi.yaml` |
| 部署 SHA / commit | `<fill at deployment time>` |
| 服务版本 | 从 `GET /api/status` 响应体 `version` 字段获取：`<fill>` |
| 数据库类型与版本 | 主库：SQLite / MySQL（>= 5.7.8）/ PostgreSQL（>= 9.6）之一，实际版本：`<fill>`；日志库（如启用 ClickHouse）：`<fill or N/A>` |
| 部署时间 | `<fill>` |
| TLS 终止位置 | `<deployment-specific, e.g. CDN / 反向代理 / 直连>` |

> 准入要求：Base URL 必须为 `https://`，禁止以 `http://` 进行登录联调（密码与 token 均走该通道）。

## 2. 测试账号

| 账号类型 | 用途 | username | password | 其他 | 获取方式 | 重置方式 |
| --- | --- | --- | --- | --- | --- | --- |
| 普通账号（无 TOTP） | 登录成功、加密登录、刷新、退出主流程 | `<fill>` | `<fill>` | — | 管理员创建 | 管理员重置密码 |
| TOTP 账号（已启用 2FA） | challenge / verify / 错误码 / 锁定 | `<fill>` | `<fill>` | TOTP secret = `<fill>` | 管理员创建并为其绑定启用 TOTP | 管理员清除 TOTP 后由用户重新绑定 |
| 限流异常账号 | 触发 429 | `<fill>` | `<fill>` | — | 同普通账号 | 等待限流窗口过期（`CriticalRateLimitDuration` 秒，默认 1200 秒，环境变量 `CRITICAL_RATE_LIMIT_DURATION`） |
| 锁定账号（TOTP 连续失败锁定） | 验证锁定后正确码仍被拒 | `<fill>` | `<fill>` | — | 对 TOTP 账号连续提交 5 次错误 TOTP 码触发（`MaxFailAttempts=5`） | 等待 300 秒（`LockoutDuration=300`）自动解锁，或管理员调用 `ResetFailedAttempts` |

> 账号口令、TOTP secret 一律填写到保密台账（1Password / 内部密码库），**不得**粘贴到本文件或测试日志中。

## 3. Turnstile 模式

| 项 | 值 |
| --- | --- |
| 模式 | `<disabled | test-keys | production>`（部署时选择其一） |
| Site Key | `<deployment-specific>` |
| Secret Key | `<deployment-specific, server-side only>`（仅服务端持有，不入库、不下发客户端） |
| 验证 URL（固定） | `https://challenges.cloudflare.com/turnstile/v0/siteverify` |
| 承载页 URL | 见 [desktop-turnstile-integration.md](./desktop-turnstile-integration.md) 第 3 章 |

- 当 `TurnstileCheckEnabled=false` 时，登录请求体中 `captcha_token` 可省略，服务端直接跳过校验（`middleware.ValidateTurnstileToken` 短路返回 nil）。
- 当 `TurnstileCheckEnabled=true` 时：缺 `captcha_token` → `403 AUTH_CAPTCHA_REQUIRED`；token 被 Cloudflare 拒绝 → `400 AUTH_CAPTCHA_INVALID`；siteverify 网络错误 → `503 AUTH_CAPTCHA_UNAVAILABLE`。
- 测试环境建议使用 Cloudflare **test keys**（always-pass / always-fail）以便自动化覆盖上述三条分支。

## 4. 用例执行状态表

> 状态枚举：`PASS` / `FAIL` / `PENDING` / `BLOCKED`。每条用例的"证据链接"列必须附测试日志、抓包或截图的可访问 URL。

| 用例ID | 用例名称 | 端点 | 预期状态码 | 预期 code | 实际结果 | 证据链接 | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| LOGIN-001 | 普通账号登录成功（明文密码，加密关闭时） | `POST /api/desktop/auth/login` | 200 | `OK` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-002 | TOTP 账号登录返回 challenge（不发 bundle） | `POST /api/desktop/auth/login` | 200 | `AUTH_VERIFICATION_REQUIRED`（data 含 `flow_token`/`expires_at`/`methods=["totp"]`） | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-003 | 用正确 TOTP 码完成二次验证 | `POST /api/desktop/auth/verify` | 200 | `OK`（返回 bundle） | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-004 | TOTP 错误码 | `POST /api/desktop/auth/verify` | 401 | `AUTH_VERIFICATION_FAILED` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-005 | TOTP 锁定中提交正确码仍被拒 | `POST /api/desktop/auth/verify` | 401 | `AUTH_VERIFICATION_FAILED` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-006 | 加密密码（v2 RSA-OAEP+AES-GCM）登录成功 | `POST /api/desktop/auth/login` | 200 | `OK` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-007 | 加密 key stale（kid 与服务端当前 kid 不匹配） | `POST /api/desktop/auth/login` | 409 | `AUTH_ENCRYPTION_KEY_STALE` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-008 | Turnstile 开启但未带 captcha_token | `POST /api/desktop/auth/login` | 403 | `AUTH_CAPTCHA_REQUIRED` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-009 | captcha_token 被 Cloudflare 拒绝 | `POST /api/desktop/auth/login` | 400 | `AUTH_CAPTCHA_INVALID` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-010 | 密码错误 / 用户不存在（统一错误） | `POST /api/desktop/auth/login` | 401 | `AUTH_INVALID_CREDENTIALS` | `<fill>` | `<fill>` | `<fill>` |
| LOGIN-011 | 设备元数据缺失或非法（如 platform 非 win32/darwin） | `POST /api/desktop/auth/login` | 400 | `INVALID_ARGUMENT` | `<fill>` | `<fill>` | `<fill>` |
| REFRESH-001 | 正常轮换（旧 RT 换新 RT+AT） | `POST /api/desktop/auth/refresh` | 200 | `OK` | `<fill>` | `<fill>` | `<fill>` |
| REFRESH-002 | 30s 容错窗口内旧 token 重放 | `POST /api/desktop/auth/refresh` | 200 | `OK`（容错，返回新 bundle） | `<fill>` | `<fill>` | `<fill>` |
| REFRESH-003 | 超窗重放旧 token | `POST /api/desktop/auth/refresh` | 401 | `AUTH_SESSION_REVOKED` | `<fill>` | `<fill>` | `<fill>` |
| REFRESH-004 | 随机/伪造 refresh_token | `POST /api/desktop/auth/refresh` | 401 | `AUTH_UNAUTHORIZED` | `<fill>` | `<fill>` | `<fill>` |
| REFRESH-005 | session 绝对到期（超过 7 天 TTL） | `POST /api/desktop/auth/refresh` | 401 | `AUTH_SESSION_EXPIRED` | `<fill>` | `<fill>` | `<fill>` |
| LOGOUT-001 | 正常退出 | `POST /api/desktop/auth/logout` | 200 | `OK`（data.`logged_out=true`） | `<fill>` | `<fill>` | `<fill>` |
| LOGOUT-002 | 重复退出（幂等） | `POST /api/desktop/auth/logout` | 200 | `OK` | `<fill>` | `<fill>` | `<fill>` |
| LOGOUT-003 | 携带的 access token 与 RT 不匹配 | `POST /api/desktop/auth/logout` | 409 | `AUTH_SESSION_MISMATCH` | `<fill>` | `<fill>` | `<fill>` |
| LOGOUT-004 | 仅 SID、无有效 refresh_token | `POST /api/desktop/auth/logout` | 401 / 400 | `AUTH_UNAUTHORIZED` / `INVALID_ARGUMENT` | `<fill>` | `<fill>` | `<fill>` |
| RATE-001 | 超限触发限流 | `POST /api/desktop/auth/login` | 429 | `AUTH_RATE_LIMITED`（响应头含 `Retry-After`） | `<fill>` | `<fill>` | `<fill>` |
| STATUS-001 | 服务状态探测 | `GET /api/status` | 200 | —（JSON，含 `version`） | `<fill>` | `<fill>` | `<fill>` |
| ENCKEY-001 | 拉取加密公钥 | `GET /api/user/login/encryption-key` | 200 | —（data 含 `enabled`/`kid`/`public_key`） | `<fill>` | `<fill>` | `<fill>` |

**通过准则**：上述 24 条用例全部 `PASS`，或所有 `FAIL`/`BLOCKED` 均有经后端负责人签字认可的风险接受记录，方可视为联调准入通过。

## 5. 阻塞项

| 阻塞项 | 影响用例 | 当前状态 | 解除条件 |
| --- | --- | --- | --- |
| Turnstile Site Key / Secret Key 未配置 | LOGIN-008 / LOGIN-009 | `<BLOCKED | filled>` | 在 Cloudflare 后台创建 Site Key 并配置到服务端环境变量，secret 仅服务端持有 |
| 部署 URL 未确定 | 全部 | `<BLOCKED | filled>` | 提供 `https://` Base URL 并完成 TLS 证书校验 |
| 测试账号未创建（普通 / TOTP / 限流 / 锁定） | 全部 | `<BLOCKED | filled>` | 管理员按第 2 章创建并把口令置入保密台账 |
| MySQL / PostgreSQL 远端 CI 验证待执行 | REFRESH / LOGOUT 落库路径 | `<PENDING>` | 在真实 MySQL>=5.7.8、PostgreSQL>=9.6 实例上跑迁移与会话轮换回归（SQLite 单跑不算完成） |

## 6. 待冻结/阻塞错误码

> 本节逐项登记桌面端认证契约中出现的错误码及其当前实现状态。**已冻结** = 由 `controller/desktop_auth.go` + `service/auth_session.go` 的 handler 实际产生，且已被 `controller/desktop_auth_openapi_test.go` 的 knownAuthCodes 漂移测试锁定；**待冻结** = 尚未由任何 handler 产生，属于部署/网关层约定，在冻结前不得被当作 handler 契约依赖。

| 错误码 | 主状态码 | 含义 | 实现状态 |
| --- | --- | --- | --- |
| `OK` | 200 | 成功 | 已冻结（handler 产生） |
| `INVALID_ARGUMENT` | 400 | 请求字段缺失/非法 | 已冻结（handler 产生） |
| `AUTH_PASSWORD_LOGIN_DISABLED` | 403 | 管理员关闭密码登录 | 已冻结（handler 产生） |
| `AUTH_CAPTCHA_REQUIRED` | 403 | 开启 Turnstile 但缺 `captcha_token` | 已冻结（handler 产生） |
| `AUTH_CAPTCHA_INVALID` | 400 | Cloudflare 拒绝 token | 已冻结（handler 产生） |
| `AUTH_CAPTCHA_UNAVAILABLE` | 503 | siteverify 网络错误或 5xx | 已冻结（handler 产生） |
| `AUTH_ENCRYPTION_PAYLOAD_INVALID` | 400 | RSA-OAEP+AES-GCM 密文非法 | 已冻结（handler 产生） |
| `AUTH_ENCRYPTION_KEY_STALE` | 409 | `kid` 与服务端当前公钥不匹配 | 已冻结（handler 产生） |
| `AUTH_INVALID_CREDENTIALS` | 401 | 密码错误 / 用户不存在（统一错误） | 已冻结（handler 产生） |
| `AUTH_VERIFICATION_UNSUPPORTED` | 403 | 请求的二次因子系统未配置或被禁用（注意：TOTP 已配置但被临时锁定不属于此项） | 已冻结（handler 产生） |
| `AUTH_VERIFICATION_REQUIRED` | 200 | 登录需二次验证（challenge） | 已冻结（handler 产生） |
| `AUTH_FLOW_EXPIRED` | 401/409 | challenge 过期、已消费或不存在 | 已冻结（handler 产生） |
| `AUTH_VERIFICATION_FAILED` | 401 | 错误 TOTP 码，或 TOTP 已配置但处于锁定窗口 | 已冻结（handler 产生） |
| `AUTH_SESSION_LIMIT` | 409 | 用户会话数超限 | 已冻结（handler 产生） |
| `AUTH_SESSION_ISSUANCE_LIMIT` | 409 | 会话签发限流 | 已冻结（handler 产生） |
| `AUTH_SESSION_MISMATCH` | 409 | access token 与 refresh token 不匹配 | 已冻结（handler 产生） |
| `AUTH_REFRESH_RACE` | 409 | 并发刷新竞争 | 已冻结（handler 产生） |
| `AUTH_TOKEN_EXPIRED` | 401 | access token 过期 | 已冻结（handler 产生） |
| `AUTH_SESSION_EXPIRED` | 401 | session 超过绝对 TTL（7 天），且未被吊销 | 已冻结（handler 产生） |
| `AUTH_SESSION_REVOKED` | 401 | session 被吊销、不存在或被登出 | 已冻结（handler 产生） |
| `AUTH_UNAUTHORIZED` | 401 | 伪造/非法 refresh_token 或 access token | 已冻结（handler 产生） |
| `AUTH_RATE_LIMITED` | 429 | 触发 `DesktopAuthRateLimit` | 已冻结（handler/中间件产生） |
| `AUTH_INTERNAL_ERROR` | 500 | 内部错误兜底 | 已冻结（handler 产生） |
| `SERVICE_UNAVAILABLE` | 503 | 服务整体不可用 | **待冻结：当前 main 未实现，任何 handler 都不产生该 code；它属于部署/网关层（负载均衡、熔断、5xx 兜底）约定，OpenAPI 中仅以可选 503 响应标注，冻结前不得被客户端当作后端 handler 契约断言** |

## 7. 签名

| 角色 | 姓名 | 日期 |
| --- | --- | --- |
| 后端负责人 | `<name>` | `<date>` |
| 桌面端负责人 | `<name>` | `<date>` |
| QA | `<name>` | `<date>` |
