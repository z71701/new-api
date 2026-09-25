# 桌面端认证 OWASP 适用控制与验证记录

> 基线：OWASP **ASVS v5.0.0**（2025-05 发布）、[Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)。
>
> 说明：ASVS v5.0.0 相较 4.0 重构了章节编号（Authentication 为 V6、Session Management 为 V7、Cryptography 为 V11、Secure Communication 为 V12、Security Logging 为 V16、Web Frontend Security 为 V3、Data Protection 为 V14）。下表 ID 均取自 v5.0.0 正式发布版需求原文。
>
> 状态枚举：`IMPLEMENTED+VERIFIED`（已实现并有自动化测试/联调用例验证）、`IMPLEMENTED`（已实现，待联调或部署侧验证）、`NOT_APPLICABLE`（桌面端架构不适用，附理由）、`BLOCKED`（依赖部署侧配置）。

## 1. 适用控制矩阵

| ASVS ID | 控制要求（v5.0.0 原文摘要） | 实现位置 | 验证方式 | 状态 |
| --- | --- | --- | --- | --- |
| V12.2.1 | 客户端到对外 HTTP 服务的所有连接使用 TLS，且不回退到明文 | 全链路 HTTPS 部署；登录密码在 TLS 之上再做应用层加密（`common/password_crypto.go` v2 信封） | 联调准入单 STATUS-001 / LOGIN-001；部署侧确认仅 443 对外 | IMPLEMENTED（传输依赖部署侧） |
| V11.4.2 | 密码使用批准的、计算密集型 KDF 存储，参数按当前指引配置 | `common/account_password.go`：新密码 argon2id（m=19456,t=2,p=1）；`common/crypto.go`：bcrypt 兼容旧哈希，`ValidatePasswordAndHash` 统一校验 | `model` 用户密码哈希相关单测；登录路径 `user.ValidateAndFill` | IMPLEMENTED+VERIFIED |
| V6.3.1 | 实现抵御 credential stuffing / 密码爆破的控制 | `middleware/rate-limit.go` `DesktopAuthRateLimit()`（IP 维度，`CriticalRateLimitNum`/`CriticalRateLimitDuration`，默认窗口 1200s）；TOTP 锁定 `model/twofa.go` `IncrementFailedAttempts`（5 次/300s） | `middleware/rate_limit_test.go` `TestDesktopAuthRateLimitReturnsStructuredError`；准入单 RATE-001 | IMPLEMENTED+VERIFIED |
| V6.6.3 | 基于码的带外/OTP 机制用限流防爆破 | 同 DesktopAuthRateLimit 覆盖 `/verify`；TOTP 失败计数锁定（5/300s） | 准入单 LOGIN-004/005；`model/twofa.go` | IMPLEMENTED+VERIFIED |
| V6.3.3 | L2 必须使用 MFA | `service.StartDesktopLoginVerification` → 对已绑 TOTP 账号下发 challenge；`controller/desktop_auth.go` `DesktopVerify` | `controller/auth_session_test.go` `TestDesktopLoginReturnsVerificationChallengeAndCompletesTOTP`；`desktop_auth_acceptance_test.go` `TestDesktopAcceptanceTOTPChallengeAndVerify` | IMPLEMENTED+VERIFIED |
| V6.5.1 | TOTP / lookup secret 只能成功使用一次 | TOTP flow token 一次性消费（`ErrAuthFlowConsumed` 路径）；备用码一次性 | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceTOTPReplay` | IMPLEMENTED+VERIFIED |
| V6.5.3 | TOTP seed 用 CSPRNG 生成 | TOTP secret 由 `common` TOTP 签发流程生成（`common/totp.go`） | 安全 enrollment 测试 `controller/security_enrollment_test.go` `TestSecurityEnrollmentTwoFAFlowAndSessionRotation` | IMPLEMENTED |
| V7.2.3 | 引用型 session token 唯一、CSPRNG 生成、≥128 bits 熵 | `service/auth_session.go` `newLoginSession`：refresh secret = `common.GenerateRandomCharsKey(64)`（CSPRNG，62 字符表 × 64 ≈ 381 bits）；SID = `uuid.NewString()`；DB 仅存 HMAC(refresh secret) | `service/auth_session_test.go` `TestLoginSessionCreateRefreshAndRevoke` | IMPLEMENTED+VERIFIED |
| V11.5.1 | 不可猜测的随机数用 CSPRNG、≥128 bits 熵 | refresh secret、flow token、request ID 均走 `crypto/rand`（`common/utils.go` `GenerateRandomCharsKey` 用 `crand.Reader`） | 同上；`common/utils.go` | IMPLEMENTED+VERIFIED |
| V7.3.2 | 存在绝对最长 session 寿命 | `DesktopLoginSessionTTL = 7 * 24h`（`service/auth_session.go`）；access token `AccessTokenTTL = 15min`；超期 → `ErrLoginSessionInvalid` → 401 `AUTH_SESSION_EXPIRED` | 准入单 REFRESH-005；`service/auth_session_test.go` | IMPLEMENTED+VERIFIED |
| V7.4.1 | 登出/过期后 session 立即失效，后端销毁 | `service.RevokeDesktopByRefreshTokenStrict`；`model.RevokeUserSessionByRefreshHash`；登出幂等 | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceLogoutSuccess`、`TestDesktopAcceptanceLogoutInvalidRefreshToken` | IMPLEMENTED+VERIFIED |
| V7.4.3 | 更换/移除认证因子后可终止其他活跃 session | 改密/启停 2FA 时 `BumpUserAuthVersion` 递增 auth version，`RevokeOtherUserSessions` 撤销其余 session；`ValidateLoginSession` 校验 `UserAuthVersion` | `controller/security_account_test.go` `TestSecurityAccountLongUnicodePasswordAndSessionRotation`；`model/user_cache_auth_version_test.go` | IMPLEMENTED+VERIFIED |
| V10.4.5 | 公共客户端用 refresh token rotation 缓解重放；重用已失效 RT 即撤销该授权下全部 RT | `service/refreshLoginSession`：`RotateUserSessionRefresh` 原子轮换，`RefreshReplayWindow=30s` 容错窗口；超窗重用 `ErrUserSessionRefreshReuse` → 撤销整个 session | `service/auth_session_test.go` `TestDesktopRefreshRotationSecurity`、`TestDesktopConcurrentRefreshReturnsSingleSuccessor`；`model/user_session_test.go` `TestRotateUserSessionRefreshRaceAndReuse` | IMPLEMENTED+VERIFIED |
| V3.3.1 / V3.3.2 / V3.3.4 | cookie 需 Secure / SameSite / HttpOnly | 桌面端**不使用 cookie**：access token 走 `Authorization: Bearer` 头，refresh token 走请求体；响应断言不写 `Set-Cookie` | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceNoSetCookie` | NOT_APPLICABLE（无 cookie，故无 HttpOnly/Secure/SameSite 可言；凭证经 header+body 传输） |
| V6.3.8 | 不得从错误消息、HTTP 状态码、响应时长推断合法用户 | 登录失败统一 401 `AUTH_INVALID_CREDENTIALS`，不区分"用户不存在"与"密码错误"（`controller/desktop_auth.go`） | `controller/auth_session_test.go` `TestDesktopAuthRejectsInvalidCredentialsAndCrossClientRefresh` | IMPLEMENTED（文案/状态码层）；响应时长侧信道见"未覆盖缺口" |
| V16.5.1 | 意外/敏感错误返回通用消息，不泄露堆栈、密钥、token | `writeDesktopAuthResponse` 统一 envelope；内部错误映射为 `AUTH_INTERNAL_ERROR`，不带堆栈 | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceResponseShellFields`、`TestDesktopAcceptanceInternalErrorOnDBFailure` | IMPLEMENTED+VERIFIED |
| V16.3.1 | 记录所有认证操作（成功**与失败**），含因子类型元数据 | `controller/user.go` `recordLoginAudit` 记录成功登录（method、IP、UA、是否经二次验证） | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceAuditExcludesPassword`、`TestDesktopAcceptanceAuditExcludesEncryptedPassword` | IMPLEMENTED（成功路径）；失败路径缺口见下节 |
| V16.2.5 | 日志不得记录凭证；session token 只能哈希/掩码 | 审计日志仅记 `method`/`verification_method`/IP/UA；密码与加密密码均不入日志（有测试断言） | 上述两个 AuditExcludes* 测试 | IMPLEMENTED+VERIFIED |
| V14.3.2 | 敏感响应设置 `Cache-Control: no-store` | `controller/auth_session.go` `setAuthNoStore` 在四个桌面端点统一下发 `Cache-Control: no-store` | `desktop_auth_acceptance_test.go` `TestDesktopAcceptanceNoStoreHeader` | IMPLEMENTED+VERIFIED |
| V3.4.1 | 所有响应带 HSTS（max-age≥1 年） | 由反向代理/CDN 部署层下发，Go 业务代码不负责 | 部署侧确认 | BLOCKED（部署相关） |

> Cheat Sheet 对照：TLS 传输（Authentication Cheat Sheet "Transmit Passwords Only Over TLS"）、密码安全存储（"Store Passwords in a Secure Fashion"）、通用错误消息防枚举（"Authentication and Error Messages"）、登录节流与账号锁定（"Login Throttling"）、session ID ≥64 bits 熵与 CSPRNG（Session Management Cheat Sheet "Session ID Entropy"）、idle/absolute 超时（"Session Expiration"）、登出服务端失效（"Manual Session Expiration"）、`Cache-Control: no-store`（"Web Content Caching"）。

## 2. 未覆盖缺口

| 缺口 | 对应要求 | 现状与原因 | 建议 |
| --- | --- | --- | --- |
| 失败登录未入审计 | V16.3.1（要求成功与失败都记录） | `recordLoginAudit` 当前仅在登录成功时调用（`controller/user.go` 注释明确"仅记录成功，不记录失败"）；错误密码、TOTP 失败、429 未进入该审计链路 | 在 `DesktopLogin`/`DesktopVerify` 的失败分支补安全事件日志（不含凭证），并加回归测试 |
| 响应时长侧信道 | V6.3.8（L3，含响应时长不可区分） | 用户不存在时 `ValidateAndFill` 提前失败、已知用户密码错误时才做 argon2/bcrypt 比较，两者耗时不同；当前仅在文案/状态码层统一 | 对不存在用户做一次哑哈希比较以拉平耗时，或接受 L3 缺口并记录风险 |
| 空闲超时 | V7.3.1（inactivity timeout） | 桌面 session 只有 7 天绝对 TTL，无独立空闲超时；refresh 轮换隐式延长有效期 | 评估是否为桌面长会话补充 idle 上限，或在文档中以绝对 TTL + 主动登出作为接受方案 |
| 可疑登录通知 | V6.3.5 / V6.3.7（L3） | 异地/异常登录、认证信息变更后的用户通知未实现 | 如需 L3 强度，补邮件/站内通知 |
| HSTS 头 | V3.4.1 | 业务代码不下发，依赖反向代理/CDN | 部署侧确认 `Strict-Transport-Security` 已配置并纳入准入单 |
| 登出 UI | V7.4.4（可见登出入口） | 后端提供 `logout` 端点；桌面端是否在各处提供可见登出按钮属于客户端 UX 范围 | 桌面端自行验收，不在后端控制面 |

## 3. 参考来源

- OWASP ASVS v5.0.0 需求原文：<https://github.com/OWASP/ASVS/tree/v5.0.0/5.0/en>（V6 Authentication、V7 Session Management、V11 Cryptography、V12 Secure Communication、V3 Web Frontend Security、V14 Data Protection、V16 Security Logging and Error Handling）
- Authentication Cheat Sheet：<https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html>
- Session Management Cheat Sheet：<https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html>
