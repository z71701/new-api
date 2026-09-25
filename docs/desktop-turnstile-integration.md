# 桌面端 Turnstile Electron 集成规范

> 本文档规定桌面客户端（Electron Main / Renderer）如何采集 Cloudflare Turnstile token，并通过 `POST /api/desktop/auth/login` 的 `captcha_token` 字段提交给后端。所有部署相关值以 `<deployment-specific...>` 占位符给出，**严禁**将真实 Site Key、Secret Key、承载页域名提交入库。
>
> 服务端对应实现：`middleware/turnstile-check.go`（`ValidateTurnstileToken`）、`controller/desktop_auth.go`（`DesktopLogin` 中的 Turnstile 分支）。

## 1. 架构概述

```
Electron Main
  │  1. 弹出（或隐藏）一个 BrowserWindow
  ▼
加载 Turnstile 承载页（HTTPS）
  │
Renderer（承载页）执行 Turnstile widget
  │  2. widget 校验通过后回调 window.desktopTurnstile.submitToken(token)
  ▼
preload script（contextBridge）
  │  3. ipcRenderer.send('desktop:turnstile-token', token)
  ▼
Electron Main（ipcMain.once 收到 token）
  │  4. 立即关闭 BrowserWindow
  ▼
Main 发起 POST <base URL>/api/desktop/auth/login
   body: { ..., "captcha_token": "<token>" }
```

- token 只在 Main 进程内存中短暂存在，不写盘、不入日志、不持久化。
- 当后端返回 `403 AUTH_CAPTCHA_REQUIRED`（服务端开启了 Turnstile 但本次未带 token）时，桌面端必须走一次上述采集流程后重试登录。

## 2. 唯一 HTTPS Origin

- 承载页必须通过 **HTTPS** 提供，Origin 唯一：`<deployment-specific origin, e.g. https://turnstile.example.com>`。
- **禁止**用 `file://` 或 `http://` 加载 Turnstile（Cloudflare 要求 HTTPS + 已注册域名）。
- Turnstile widget 的 hostname 必须在 Cloudflare 后台的 Site Key hostname allowlist 中注册（见第 4 章）。

## 3. 承载页 URL

- 完整承载页 URL：`<deployment-specific full URL, e.g. https://turnstile.example.com/desktop-turnstile.html>`。
- 承载页必须引入 Turnstile 官方 JS：

```html
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
```

- JS URL 固定为 `https://challenges.cloudflare.com/turnstile/v0/api.js`，**不得镜像或自托管**。
- widget 渲染参数：
  - `sitekey="<deployment-specific site key>"`
  - `theme="light"`
  - `callback`：调用 `window.desktopTurnstile.submitToken(token)` 的函数。

最小骨架（示意，不是占位真实 key）：

```html
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <!-- CSP 见第 7 章 -->
  <title>Human Verification</title>
  <script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
</head>
<body>
  <div class="cf-turnstile"
       data-sitekey="<deployment-specific site key>"
       data-theme="light"
       data-callback="onTurnstile"></div>
  <script>
    function onTurnstile(token) {
      window.desktopTurnstile.submitToken(token);
    }
  </script>
</body>
</html>
```

## 4. Hostname Allowlist

- Cloudflare Site Key 配置的 hostname allowlist：`<deployment-specific list, e.g. turnstile.example.com>`。
- 桌面端 BrowserWindow 实际加载 URL 的 hostname 必须命中 allowlist 中的某一项。
- `localhost` / `127.0.0.1` **仅允许本地开发**；生产构建必须使用已注册的真实域名，否则 widget 直接渲染失败。

## 5. Electron BrowserWindow 安全配置

```javascript
new BrowserWindow({
  show: false, // 或 true，取决于 UX（可见时需有明确标题与关闭按钮）
  width: 300,
  height: 400,
  webPreferences: {
    nodeIntegration: false,      // 强制
    contextIsolation: true,       // 强制
    sandbox: true,                // 强制
    javascript: true,             // Turnstile 需要 JS
    devTools: false,               // 生产环境关闭
    preload: path.join(__dirname, 'preload.js'),
  },
});
```

- `nodeIntegration: false`（强制）。
- `contextIsolation: true`（强制）。
- `sandbox: true`（强制）。
- 生产环境 `devTools: false`。
- 不要在该窗口里加载任何业务页面；它只承载 Turnstile。

## 6. IPC 通道

- 通道名固定：`desktop:turnstile-token`。
- 消息方向：Renderer → Main；payload 为**纯 string**（Turnstile token），不要包装成对象。
- Main 端监听（一次性）：

```javascript
ipcMain.once('desktop:turnstile-token', (event, token) => {
  // token 仅用于本次登录请求，用完即弃
  closeTurnstileWindow();
  proceedLogin(token);
});
```

- Renderer 端通过 preload script 的 `contextBridge` 暴露受控接口：

```javascript
contextBridge.exposeInMainWorld('desktopTurnstile', {
  submitToken: (token) => ipcRenderer.send('desktop:turnstile-token', token),
});
```

- token 传回 Main 后**立即关闭 BrowserWindow**，不持久化 token、不写磁盘、不打日志。

## 7. CSP（Content Security Policy）

承载页必须通过响应头或 `<meta>` 设置 CSP：

```
default-src 'self';
script-src 'self' https://challenges.cloudflare.com;
frame-src https://challenges.cloudflare.com;
style-src 'self' 'unsafe-inline';
img-src 'self' data: https://challenges.cloudflare.com;
connect-src 'self' https://challenges.cloudflare.com;
```

- 不得使用 `unsafe-eval`。
- `script-src` 必须精确包含 `challenges.cloudflare.com`，不要用通配符放开整个 cloudflare.com。

## 8. 导航 / 请求白名单

- 在 BrowserWindow 的 `will-navigate` 事件中限制目标 URL，仅允许：
  - `<deployment-specific origin>/*`（承载页自身）；
  - `https://challenges.cloudflare.com/*`（Turnstile）。
- 任何其他导航立即 `event.preventDefault()` 并关闭窗口。
- 通过 `webRequest.onBeforeRequest` 将出站请求同样限制在上述两个域名。

## 9. 服务端验证

- 服务端在登录时调用 Cloudflare：`POST https://challenges.cloudflare.com/turnstile/v0/siteverify`（URL 固定，见 `middleware/turnstile-check.go`）。
- 表单参数：
  - `secret`：服务端 Secret Key（`<deployment-specific, server-side only>`）；
  - `response`：客户端提交的 `captcha_token`；
  - `remoteip`：客户端 IP（`c.ClientIP()`）。
- 处理结果：
  - `success=true` → 放行登录；
  - `success=false`（token 被拒）→ 返回 `400 AUTH_CAPTCHA_INVALID`；
  - siteverify 网络错误 / 非 2xx → 返回 `503 AUTH_CAPTCHA_UNAVAILABLE`。
- 当 `TurnstileCheckEnabled=false` 时，`captcha_token` 可省略，服务端短路放行。

## 10. 未知值 / 阻塞项

| 项 | 状态 | 说明 |
| --- | --- | --- |
| Site Key | `<BLOCKED>` | 由部署方在 Cloudflare 后台申请后填入承载页与 Cloudflare 配置 |
| Secret Key | `<BLOCKED>` | 仅服务端环境变量持有，不下发桌面端、不入库 |
| 承载页 Origin / URL | `<BLOCKED>` | 由部署方提供并注册到 Cloudflare allowlist |
| Hostname allowlist | `<BLOCKED>` | 必须与 BrowserWindow 实际加载 hostname 严格一致 |
