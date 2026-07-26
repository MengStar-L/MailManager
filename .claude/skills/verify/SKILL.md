---
name: verify
description: 启动真实 MailManager 服务并驱动收信/事件链路做端到端验证的项目配方
---

# MailManager 端到端验证配方

## 构建与启动

```bash
cd web && npm run build            # 前端产物输出到 internal/webui/dist（go:embed）
go build -o bin/mailmanager-verify.exe ./cmd/mailmanager   # 必须在前端构建之后
MAILMANAGER_DATA_DIR=/tmp/mmverify/data \
MAILMANAGER_ADDR=127.0.0.1:18973 \
MAILMANAGER_PUBLIC_URL=http://localhost:18973 \
./bin/mailmanager-verify.exe serve
```

要点：
- 端口 18080 在这台机器上被 CF-R2Manager 占用，换 18973 等高位端口。
- `MAILMANAGER_PUBLIC_URL` 的主机名必须和浏览器地址栏一致（localhost vs
  127.0.0.1 不同源会触发"请求来源不受信任"的 Origin 校验），curl 不带
  Origin 头不受影响。

## 初始化与登录（脚本化）

- bootstrap token 在 `<data>/bootstrap-token`。
- 流程：`POST /api/v1/setup/enroll` {bootstrap_token, username} → 返回 TOTP
  secret → `POST /setup/complete` {bootstrap_token, password, totp_code} →
  `POST /auth/login` → `POST /auth/totp` → 拿到 `mailmanager_session` +
  `mailmanager_csrf` cookie。
- TOTP 用 python hmac/sha1 30 秒窗口现算；写操作需带 `X-CSRF-Token` 头
  （值 = csrf cookie）。

## 有用的观察面

- SSE：`curl -N -H "Cookie: ..." /api/v1/events`，心跳事件
  `{"type":"heartbeat"}` 每 20 秒一个。
- 卡死探针：本地起一个假 IMAP 服务器（accept 后发 `* OK ready` greeting 然后
  永久沉默），用 provider `"imap"`、`tls_mode:"starttls"` 建账户指向它
  （请求必须带 `color:"#RRGGBB"`，否则 422）。健康行为：30 秒整被握手
  watchdog 掐断，日志出现 "mail sync job failed ... unexpected EOF"，账户
  状态在 error/syncing 间按重试循环。
- 浏览器（preview 工具）：React 受控输入必须用原生 value setter + input
  事件（preview_fill 不触发 onChange）。账户同步失败 ~30 秒内侧栏应自动
  变"同步异常"（DB→事件→SSE→refetch 全链路）。杀后端进程会连带销毁
  preview 浏览器会话，SSE 断线重连无法用该方式观察。
