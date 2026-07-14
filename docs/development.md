# MailManager 开发指南

本文面向参与 MailManager 开发、测试和发版的维护者。普通部署请阅读项目根目录的 `README.md`。

## 环境要求

- Go 1.26
- Node.js 24
- npm 11

## 本地运行

构建前端并启动嵌入式服务：

```powershell
npm ci --prefix web
npm run build --prefix web
go run ./cmd/mailmanager serve
```

后端默认监听 `0.0.0.0:8080`，浏览器仍可通过 `http://127.0.0.1:8080` 访问；开发环境数据写入仓库根目录的 `data/`。

前端热更新：

```powershell
npm run dev --prefix web
go run ./cmd/mailmanager serve
```

Vite 会把 `/api`、`/healthz` 和 `/readyz` 代理到 Go 服务。

## 测试

```powershell
go test ./...
go vet ./...
npm test --prefix web -- --reporter=dot
npm run test:e2e --prefix web
npm run build --prefix web
```

Linux 部署脚本还需要：

```bash
sh -n scripts/install-linux.sh scripts/install-linux_test.sh scripts/build-release.sh
sh scripts/install-linux_test.sh
shellcheck scripts/install-linux.sh scripts/install-linux_test.sh scripts/build-release.sh
```

## 构建 Release

Windows PowerShell：

```powershell
./scripts/build-release.ps1 -Version 1.1.0
```

Linux 或 macOS：

```bash
VERSION=1.1.0 sh ./scripts/build-release.sh
```

构建会生成：

- Linux amd64 和 arm64 静态二进制。
- 交互式安装器。
- systemd 单元与 Nginx 示例。
- 环境配置示例。
- `checksums.txt`。

所有文件输出到 `dist/`。构建脚本会验证两个二进制中的版本、GOOS、GOARCH 和静态 BuildMarker。

## 发布

推送不带前导零的稳定语义版本标签：

```bash
git tag -a v1.1.0 -m "MailManager v1.1.0"
git push origin v1.1.0
```

Release workflow 会运行 Go 测试、前端单测、完整 E2E、ShellCheck、systemd 模板验证和双架构构建，然后创建 GitHub Release。

只发布稳定标签。网页更新器不会跟随 `main`、草稿版或预发布版本。

## 结构概览

```text
cmd/mailmanager/       服务与更新器命令入口
internal/              认证、同步、存储、HTTP API 和更新逻辑
migrations/            SQLite 迁移
web/                   React 前端
deploy/                systemd、环境变量与 Nginx 模板
scripts/               安装和 Release 构建脚本
```
