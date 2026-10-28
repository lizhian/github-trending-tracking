# GitHub 趋势追踪

[线上站点](https://github.lizhian.cc.cd/) · [实施与迁移记录](https://github.com/lizhian/github-trending-tracking/issues/21)

无需登录的中文只读趋势站，从 GitHub Trending、HelloGithub、OpenGithubs 与 Linux.do 发现项目，记录真实 Star 变化。可切换 GitHub / Linux.do 追踪类型，搜索筛选、排序分页，查看 24 小时、3 天、7 天和 30 天趋势。保留移动端、浅深色主题、键盘操作和 URL 状态恢复。

单个 Go 应用容器提供 React/TypeScript 静态前端、同源只读 API 和串行采集循环，SQLite 整个数据目录挂载本地磁盘。镜像同时支持 `linux/amd64` 与 `linux/arm64`，服务器手动按 digest 更新；部署进度和七天后备期见 [#26](https://github.com/lizhian/github-trending-tracking/issues/26)。

## 本地运行

使用 `go.mod` 所需 Go 版本、Node.js 26 和 npm。先构建前端，再显式初始化新的开发库：

```sh
npm ci
npm run build
mkdir -p data backups
# 仅适用于没有历史数据的新开发环境。生产使用全量导入或恢复。
go run ./cmd/trending init --db data/tracking.sqlite
BACKUP_DIR="$PWD/backups" ALLOWED_ORIGIN=http://localhost:8080 npm run dev:go
```

访问 `http://localhost:8080`。前端热更新可另开 `npm run dev`，Vite 将 `/api` 代理到 Go 的 8080 端口；使用热更新时将允许来源配置为 `http://localhost:5173`。采集 Token 和可选 RSS 备用地址只在运行时提供，推荐 `_FILE` 秘密文件，见 [采集配置](docs/go-collection.md)。无 Token 时仍可读取已有数据。

启动后 HTTP 立即可读，采集等到严格晚于启动时间的下一应采时段；没有公开手动采集入口。浏览和刷新只查询已保存的数据。

## 数据与维护

两种类型独立收录和淘汰，同仓库共享一次 Star 请求，各自保留周期和历史。每类型目标 500 项，24 小时保护期和可靠观测要求优先，允许暂时超额。查询严格取最近 30 天真实观测，负净增量照常显示，缺测不补零或插值；不足两个有效快照显示“积累中”。列表最多缓存 10 分钟。

每天一次同机全量一致备份，保留三天；备份和活动数据库使用独立持久目录。恢复需停应用、保留故障文件并校验备份，重启仍等待下一轮。具体时段、趋势和淘汰规则见 [采集说明](docs/go-collection.md)、[术语表](GLOSSARY.md) 与 [架构决策](docs/adr/)。

## 检查

```sh
go test ./...
npm run typecheck
npm run build
npm test -- tests/go-parity.test.ts
```

Go 测试使用真实临时 SQLite；parity 测试把同一冻结数据和时钟交给旧 Worker/D1 与新 Go/SQLite，经公开 HTTP 比较代表性结果。旧 Worker、D1 测试和共享契约保留作迁移基线。

## 部署

- [本地容器、全量导入、备份与恢复](docs/go-runtime.md)
- [双平台 GHCR 版本镜像与手动更新](docs/image-release.md)
- [加州服务器切流、回退及七天收尾](docs/production-cutover.md)
- [旧 Cloudflare 流程（仅迁移与临时回退）](docs/legacy/cloudflare-deployment.md)

使用既有服务器及批准的固定费用，继续禁止新增自动按量扣费。旧 Cloudflare 部署脚本仅用于迁移和后备期回退，常规更新使用 Go 镜像及 SQLite 备份。

[Linux.do](https://linux.do/)：连接开发者与 AI 爱好者的社区。
