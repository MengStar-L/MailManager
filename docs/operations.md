# MailManager 运维指南

以下命令以默认安装目录 `/opt/mailmanager` 为例。

## 服务状态与日志

```bash
sudo systemctl status mailmanager.service
sudo systemctl status mailmanager-updater.path
sudo journalctl -u mailmanager.service -f
```

最近一次启动或更新失败时：

```bash
sudo journalctl -u mailmanager.service -n 100 --no-pager
sudo journalctl -u mailmanager-updater.service -n 100 --no-pager
sudo cat /opt/mailmanager/updater/status.json
```

本机健康检查：

```bash
sudo grep '^MAILMANAGER_ADDR=' /opt/mailmanager/config/mailmanager.env
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

上面的请求使用默认端口。如果安装时选择了其他本机端口，请按 `MAILMANAGER_ADDR` 的值替换 `8080`。

## 更新

推荐在网页“设置 → 系统更新”中检查稳定版本。确认安装后，root 更新服务会：

1. 重新读取最新稳定 GitHub Release。
2. 下载当前架构的二进制并校验 SHA-256。
3. 备份现有二进制。
4. 安装新版本并重启主服务。
5. 检查 `/readyz`，失败时恢复上一版二进制。

也可以重新运行安装器进行手动升级。使用与首次安装相同的目录和公开地址：

```bash
sudo sh /tmp/install-mailmanager.sh \
  --install-dir /opt/mailmanager \
  --port 8080 \
  --public-url https://mail.example.com
```

如果首次安装使用了自定义本机端口，手动升级时应传入相同的 `--port` 值。

将 `/opt/mailmanager/config/mailmanager.env` 中的 `MAILMANAGER_AUTO_UPDATE_ENABLED` 改为 `false` 可以关闭网页安装能力；版本信息和手动检查仍会显示。

## 备份

必须备份：

- `config/mailmanager.env`
- `config/master.key`
- `data/mailmanager.db`
- `data/draft-blobs/`

附件缓存可以从邮箱服务器重新取得，不属于必要备份。为了获得一致的 SQLite 备份，先停止服务：

```bash
sudo systemctl stop mailmanager.service
sudo tar -C /opt -czf "mailmanager-backup-$(date +%F-%H%M%S).tar.gz" \
  mailmanager/config \
  mailmanager/data
sudo systemctl start mailmanager.service
```

数据库与 `master.key` 应分开保存一份离线副本。主密钥丢失后，邮箱密码、授权码和 OAuth Token 无法恢复。

## 恢复

```bash
sudo systemctl stop mailmanager.service
sudo tar -C /opt -xzf mailmanager-backup-YYYY-MM-DD-HHMMSS.tar.gz
sudo chown -R root:mailmanager /opt/mailmanager/config
sudo chown mailmanager:mailmanager /opt/mailmanager/config/master.key
sudo chmod 0750 /opt/mailmanager/config
sudo chmod 0640 /opt/mailmanager/config/mailmanager.env
sudo chmod 0600 /opt/mailmanager/config/master.key
sudo chown -R mailmanager:mailmanager /opt/mailmanager/data
sudo chmod 0700 /opt/mailmanager/data
sudo systemctl start mailmanager.service
curl --fail http://127.0.0.1:8080/readyz
```

自定义端口部署请按恢复后的 `config/mailmanager.env` 中 `MAILMANAGER_ADDR` 的值执行健康检查。

SQLite 迁移只向前执行。如果新版本已经执行不兼容迁移，二进制自动回滚后仍可能需要恢复数据库备份。

## 常见故障

### 本机健康检查正常，但域名打不开

检查 DNS、证书和 Nginx：

```bash
sudo nginx -t
sudo systemctl status nginx
curl -I https://mail.example.com
```

### 登录或写操作提示来源不受信任

浏览器地址必须与 `MAILMANAGER_PUBLIC_URL` 完全一致，包括协议、域名和非标准端口。修改环境文件后重启服务：

```bash
sudo systemctl restart mailmanager.service
```

### OAuth 回调失败

确认 OAuth 服务商登记的回调地址为：

```text
https://mail.example.com/api/v1/oauth/google/callback
https://mail.example.com/api/v1/oauth/microsoft/callback
```

同时确认 Nginx 传递了原始 `Host`，并将 `X-Forwarded-Proto` 设置为 `https`。

### 更新后服务未恢复

查看更新器状态和日志。更新器通常会自动恢复 `/opt/mailmanager/bin/mailmanager.previous`；如果数据库迁移造成兼容问题，请从运维备份恢复数据库。
