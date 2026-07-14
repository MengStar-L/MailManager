# MailManager 部署指南

本文面向准备在 Linux 服务器上长期运行 MailManager 的使用者。默认安装根目录为 `/opt/mailmanager`；如果选择了自定义目录，请将后续命令中的路径同步替换。

## 服务器要求

- 使用 systemd 的 Linux 发行版。
- amd64 或 arm64 CPU。
- 已安装 `curl`、`sha256sum` 和常见系统管理命令。
- 一个已解析到服务器的域名和有效 HTTPS 证书。
- Nginx 或其他 HTTPS 反向代理。
- root 或 sudo 权限。

MailManager 只监听 `127.0.0.1:8080`。请勿把该端口直接暴露到公网。

## 交互式安装

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://github.com/MengStar-L/MailManager/releases/latest/download/install-linux.sh \
  -o /tmp/install-mailmanager.sh && \
sudo sh /tmp/install-mailmanager.sh
```

向导会询问安装根目录和公开 HTTPS 地址，并在写入系统前显示摘要。直接回车会把文件安装到 `/opt/mailmanager`。

可以先预览结果，不下载或修改任何文件：

```bash
sh /tmp/install-mailmanager.sh \
  --dry-run \
  --install-dir /opt/mailmanager \
  --public-url https://mail.example.com
```

## 非交互式安装

自动化环境必须明确提供公开地址，并使用 `--yes` 跳过确认：

```bash
sudo sh /tmp/install-mailmanager.sh \
  --install-dir /opt/mailmanager \
  --public-url https://mail.example.com \
  --yes
```

固定安装某个稳定版本：

```bash
sudo sh /tmp/install-mailmanager.sh \
  --version v1.1.0 \
  --install-dir /opt/mailmanager \
  --public-url https://mail.example.com \
  --yes
```

`--no-start` 会安装文件并写入版本标记，但不会启动服务或执行健康检查。完成配置后运行：

```bash
sudo systemctl restart mailmanager.service mailmanager-updater.path
```

## 配置 HTTPS

安装器会把已填写域名的 Nginx 示例写到：

```text
/opt/mailmanager/config/nginx.conf.example
```

确认证书路径正确后安装配置：

```bash
sudo cp /opt/mailmanager/config/nginx.conf.example /etc/nginx/conf.d/mailmanager.conf
sudo nginx -t
sudo systemctl reload nginx
```

示例为普通 Nginx TLS 反向代理，并为实时同步事件关闭代理缓冲；如果公开地址使用非标准 HTTPS 端口，安装器也会同步渲染监听端口。使用 Caddy、Traefik 或其他代理时，也必须保留长连接并把请求转发到 `127.0.0.1:8080`。

如果通过 Certbot 申请证书，请先完成域名解析，再按所在发行版的 Certbot 文档签发证书。证书中的域名必须与 `MAILMANAGER_PUBLIC_URL` 一致。

## 初始化管理员

服务首次启动后会生成短期有效的一次性令牌：

```bash
sudo cat /opt/mailmanager/data/bootstrap-token
```

访问公开 HTTPS 地址，依次配置：

1. 管理员用户名和密码。
2. TOTP 动态验证码。
3. 离线保存恢复码。

初始化完成后，令牌文件会自动删除。

## 安装目录与权限

| 路径 | 所有者与权限 | 内容 |
| --- | --- | --- |
| `bin/` | `root:root 0755` | 当前二进制和上一版本备份 |
| `config/` | `root:mailmanager 0750` | 环境文件、Nginx 示例和主密钥 |
| `config/mailmanager.env` | `root:mailmanager 0640` | 服务配置 |
| `config/master.key` | `mailmanager:mailmanager 0600` | 凭据加密主密钥 |
| `data/` | `mailmanager:mailmanager 0700` | SQLite、草稿和附件缓存 |
| `updater/` | 受限 root 工作区 | 更新请求、状态和回滚文件 |

systemd 单元位于 `/etc/systemd/system`，`/usr/local/bin/mailmanager` 指向实际二进制。

## 公开地址限制

`MAILMANAGER_PUBLIC_URL` 必须是完整 HTTPS Origin，例如：

```text
https://mail.example.com
```

不要添加页面路径、查询参数或结尾斜杠。写请求的 `Origin` 必须与该地址一致，Google 和 Microsoft OAuth 回调地址也从这里生成。修改地址后必须同步更新 OAuth 服务商中的回调 URI 并重启 MailManager。

## 安装器参数

```text
--install-dir PATH
--public-url https://mail.example.com
--version vX.Y.Z|latest
--no-start
--yes
--dry-run
```

查看服务状态：

```bash
sudo systemctl status mailmanager.service mailmanager-updater.path
curl --fail http://127.0.0.1:8080/readyz
```
