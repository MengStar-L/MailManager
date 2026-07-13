# MailManager

MailManager 是一个面向个人自托管场景的多邮箱 Web 客户端。它把 Gmail、Outlook、QQ、163、企业邮箱和通用 IMAP/SMTP 账户集中到统一收件箱，同时保留清晰的账户归属、跨账户搜索、会话阅读、撰写、草稿、附件和批量整理能力。

后端是 Go 模块化单体，前端 React 资源嵌入同一个二进制。正式环境只监听本机 HTTP，由 Nginx 终止 HTTPS。

## 本地开发

需要 Go 1.26、Node.js 24 和 npm 11。

```powershell
npm ci --prefix web
npm run build --prefix web
go run ./cmd/mailmanager serve
```

默认地址为 `http://127.0.0.1:8080`。首次启动会在 `data/bootstrap-token` 写入权限受限的一次性初始化令牌；网页完成管理员密码、TOTP 和恢复码配置后，该文件会自动删除。

前端热更新模式：

```powershell
npm run dev --prefix web
go run ./cmd/mailmanager serve
```

Vite 会把 `/api`、`/healthz` 和 `/readyz` 代理到 Go 服务。

## 测试与发布

```powershell
go test ./...
npm test --prefix web -- --run
npm run test:e2e --prefix web
./scripts/build-release.ps1 -Version 1.0.0
```

Linux/macOS 可运行 `VERSION=1.0.0 sh ./scripts/build-release.sh`。两个脚本都会构建前端，输出 Linux amd64/arm64 二进制、systemd units、安装脚本和 `checksums.txt` 到 `dist/`。

推送格式为 `vX.Y.Z` 且各段没有前导零的标签会触发 GitHub Actions：只读权限的构建任务运行 Go、前端单元测试、完整 E2E 和生产构建，再由独立发布任务取得写权限并创建 Release。例如首次发布：

```bash
git tag -a v1.0.0 -m "MailManager v1.0.0"
git push origin v1.0.0
```

## Linux 部署

首次安装需要从可信的 GitHub Release 手动引导一次。先从同一个固定标签下载校验清单和安装脚本，确认脚本的 SHA-256 后再交给 root 执行：

```bash
VERSION=v1.0.0
BASE="https://github.com/MengStar-L/MailManager/releases/download/$VERSION"
curl --proto '=https' --tlsv1.2 -fL \
  -o checksums.txt "$BASE/checksums.txt"
curl --proto '=https' --tlsv1.2 -fL \
  -o install-linux.sh "$BASE/install-linux.sh"
awk '$2 == "install-linux.sh" { print }' checksums.txt > install-linux.sha256
test "$(wc -l < install-linux.sha256)" -eq 1
sha256sum --check install-linux.sha256
sudo sh install-linux.sh --version "$VERSION" --no-start
sudoedit /etc/mailmanager/mailmanager.env
sudo systemctl restart mailmanager.service mailmanager-updater.path
```

这项 SHA-256 校验能发现下载损坏或脚本与清单不一致，但清单和脚本来自同一 GitHub Release；仓库管理员凭据或 Release 发布链路同时失陷时，它不能替代独立数字签名。

安装脚本支持 amd64 和 arm64，会把真实二进制安装到 `/opt/mailmanager/bin/mailmanager`，并仅在 `/usr/local/bin/mailmanager` 创建 CLI 链接。它会创建受限服务用户和更新目录，使用 `/dev/urandom` 生成原始 32 字节主密钥，并保留已有数据、密钥和非更新配置。`--no-start` 不执行候选二进制，也不进行健康检查；升级时旧二进制保留在 `/opt/mailmanager/bin/mailmanager.previous`，确认配置后再重启。

`MAILMANAGER_PUBLIC_URL` 必须改成真实 HTTPS 域名。正常启动并通过健康检查后，期望版本会写入 root 所有、组只读的 `/var/lib/mailmanager-updater/installed-version`；使用 `--no-start` 时该标记记录已暂存、等待人工重启验证的版本。

复制 `deploy/nginx.conf.example` 到 Nginx 配置，替换域名和证书路径。SSE 路径必须保持 `proxy_buffering off`。完成后通过 `sudo cat /var/lib/mailmanager/bootstrap-token` 获取初始化令牌并访问公开 HTTPS 地址。

应用进程只监听 `127.0.0.1:8080`。不要把该端口直接暴露到公网。

## 自动更新

正式二进制每 6 小时检查 `MengStar-L/MailManager` 的最新稳定 Release；不会跟随 `main`、草稿版或预发布版本，也不会自行安装。管理员在网页“设置 -> 系统更新”确认后，主服务只向 `/var/lib/mailmanager-updater/inbox` 写请求，由 root 权限的 `mailmanager-updater.path` 启动一次性更新服务。

更新器会重新确认 Release、校验 SHA-256、备份旧二进制并重启 `mailmanager.service`。`/readyz` 在 60 秒内未恢复时会自动还原旧二进制；首次安装器也会进行有界就绪探测并在升级失败时恢复 `/opt/mailmanager/bin/mailmanager.previous`。数据库不会自动备份或回滚，因此升级前仍应完成数据备份。SQLite 迁移只向前执行，若新版本已经执行不兼容迁移，二进制回滚后可能仍需从运维备份恢复数据库。

官方 units 假定真实二进制位于 `/opt/mailmanager/bin/mailmanager`。如需更改路径，必须同时修改 `MAILMANAGER_UPDATE_BINARY_PATH`、`mailmanager.service` 和 `mailmanager-updater.service` 的 `ExecStart`，并用 drop-in 同步 updater 的 `ReadWritePaths`；只改环境变量会导致更新失败。`MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE` 同样必须指向 updater 可写、主服务可读的位置。

`MAILMANAGER_UPDATE_READY_URL` 必须与 `MAILMANAGER_ADDR` 的监听地址和端口一致。例如 `MAILMANAGER_ADDR=127.0.0.1:9090` 时应设置为 `http://127.0.0.1:9090/readyz`，否则成功启动也会被判定为更新失败。

```bash
systemctl status mailmanager-updater.path
journalctl -u mailmanager-updater.service
cat /var/lib/mailmanager-updater/status.json
```

将 `/etc/mailmanager/mailmanager.env` 中的 `MAILMANAGER_AUTO_UPDATE_ENABLED` 设为 `false` 可关闭网页安装能力；版本信息和手动检查仍会显示。

## OAuth

Google 和 Microsoft 账户需要为当前部署自行创建 Web OAuth 应用：

- Google 回调：`https://你的域名/api/v1/oauth/google/callback`
- Microsoft 回调：`https://你的域名/api/v1/oauth/microsoft/callback`

Client ID 与 Client Secret 在“设置 -> 邮箱账户”中填写；Secret 和 refresh token 使用主密钥加密，读取 API 永不返回 Secret。Google 外部应用处于 Testing 状态时 refresh token 可能只有 7 天有效期，长期运行应按 Google 控制台要求发布应用。

QQ、163 和多数企业邮箱使用邮箱后台生成的授权码或应用密码，不应填写网页登录密码。

## 数据与备份

SQLite 正文索引可搜索，因此数据库本身不是应用层密文。正式服务器应把 `/var/lib/mailmanager` 放在 LUKS、fscrypt 或等效加密卷上。

必要备份包括：

- `/var/lib/mailmanager/mailmanager.db`
- `/var/lib/mailmanager/draft-blobs/`
- `/etc/mailmanager/mailmanager.env`
- `/etc/mailmanager/master.key`

附件缓存 `/var/lib/mailmanager/attachments/` 可从邮箱服务器重建，不需要进入必要备份。数据库与主密钥应分开保存；丢失主密钥后邮箱凭据和 OAuth token 无法恢复。

一致性备份流程：

```bash
sudo systemctl stop mailmanager
sudo tar -C / -czf mailmanager-data.tar.gz var/lib/mailmanager/mailmanager.db var/lib/mailmanager/draft-blobs
sudo tar -C / -czf mailmanager-secrets.tar.gz etc/mailmanager/mailmanager.env etc/mailmanager/master.key
sudo systemctl start mailmanager
```

恢复时先停止服务，并分别恢复权限：`/var/lib/mailmanager` 数据目录及其内容为 `mailmanager:mailmanager`，`/etc/mailmanager/mailmanager.env` 为 `root:mailmanager 0640`，`/etc/mailmanager/master.key` 为 `mailmanager:mailmanager 0600`。然后再启动服务并检查 `/readyz`。

## 安全边界

- 管理员密码使用 Argon2id；登录必须通过 TOTP，恢复码仅可使用一次。
- 邮箱凭据、OAuth token 和 TOTP 密钥使用 AES-256-GCM 逐条加密。
- HTML 邮件经过白名单清洗并在 sandbox iframe 中显示；远程图片默认阻止。
- IMAP/SMTP 仅允许 TLS 或 STARTTLS，不提供跳过证书校验选项。
- 附件强制下载并限制为 25 MiB；缓存默认上限为 5 GiB。
- 同机 root 用户仍可读取运行中的数据，这是单机自托管模型的明确边界。

## 运维

- 健康检查：`GET /healthz`
- 就绪检查：`GET /readyz`
- 日志：`journalctl -u mailmanager`
- 手动同步：设置页账户菜单中的“立即同步”

升级前先备份。替换二进制并重启后，Go 服务会自动执行只向前的 SQLite 迁移；不要用旧版本二进制直接打开已升级数据库。
