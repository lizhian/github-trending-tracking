# Go + SQLite 运行与离线迁移

同一个 Go 进程提供原前端和只读 `/api`，数据库路径默认 `data/tracking.sqlite`。旧 Worker 与 D1 测试保留，作为迁移兼容基线。Go 不会在服务启动时创建空库，也不会重新初始化已迁移的 `sampling_policy.effectiveAt`。

## 本地运行

```sh
npm ci
npm run typecheck
npm run build
mkdir -p data
# 仅用于全新、明确无历史数据的开发环境；生产使用下方 import。
go run ./cmd/trending init --db data/tracking.sqlite
ALLOWED_ORIGIN=http://localhost:8080 go run ./cmd/trending serve
# 或前端热更新（代理 /api 到 Go 的 8080 端口）
npm run dev
```

`DATA_DIR`、`STATIC_DIR`、`LISTEN_ADDR` 可覆盖默认目录/监听地址；`--db`、`--static`、`--listen` 可明确指定。`ALLOWED_ORIGIN` 是唯一允许的浏览器 Origin；不存在 Origin 的只读请求仍可查询。没有公开采集或管理接口。

检查：`go test ./...`、`npm run typecheck`、`npm run build`。`npm test -- tests/go-parity.test.ts` 将同一冻结导出、同一时钟交给旧 Worker/D1 和新 Go/SQLite，经公开 HTTP 比较两类列表、排序筛选分页、四窗口历史与错误。测试失败时输出临时证据目录。`GTT_KEEP_PARITY=1` 可保留成功夹具供离线浏览验证。

## 冻结导出与导入

正式迁移前停止旧 Cron，确认正在执行的写入已结束。按 `docs/agents/deployment.md` 复用本机 Cloudflare 配置导出；本节命令的 source、report 均为私有文件，不提交仓库或公开 Issue。

```sh
npx wrangler d1 export DB --remote --config .wrangler/deploy.json --output /private/path/d1-frozen.sql
mkdir -p /private/path/new-data
# 确认导出包含 d1_migrations，版本必须恰为仓库现有 0001/0002/0003。
go run ./cmd/trending import \
  --source /private/path/d1-frozen.sql \
  --db /private/path/new-data/tracking.sqlite \
  --report /private/path/migration-report.json \
  --old-writer-stopped

go run ./cmd/trending verify --db /private/path/new-data/tracking.sqlite
```

只对可信的 Wrangler SQL 导出执行 import。导出先在临时 SQLite 装载，核对迁移版本与业务列，再向另一临时数据库复制三张业务表。D1 内部表不进入目标。全部物理历史原样复制，包含超过可见 30 天的记录；时间、UTC 日/小时桶、项目 ID、追踪周期、入库时间和全部状态不变。旧 lease 只有在显式确认旧写入者停止后才清除，报告记录该例外和原值摘要。

报告包含导出 SHA-256、按主键排序的行数与 JSON 内容摘要、类型/仓库/周期数量、快照点数及时间范围、周期/入库时间、effectiveAt/nextAt/blockedUntil 检查点和完整性/外键结果。只有校验一致才原子发布目标文件。已有目标一律拒绝覆盖；失败保留源导出。保留原始导出和报告作为切换证据。报告含业务状态，按私有文件保存。

启动时检查 schema version、列结构、完整性、外键、WAL 和可写性。SQLite 使用 foreign_keys、WAL 和 5 秒 busy timeout。数据目录通过内核文件锁限定一个活跃写入进程，异常退出自动释放，不使用 D1 的旧租约抢占。不要删除运行中的 `.writer.lock`，不要在同一目录启动第二个实例；数据库、WAL 和 SHM 必须一起位于本地持久挂载目录。

## 本地容器

```sh
docker build -t github-trending-tracking:local .
mkdir -p data
# 容器以 10001:10001 运行，Linux 宿主需给挂载目录相应权限。
# 全新开发库：
docker run --rm -v "$PWD/data:/data" github-trending-tracking:local init
# 已有历史则通过 import 生成 tracking.sqlite，切勿 init 替代迁移。
docker run -d --name github-trending-tracking \
  -p 127.0.0.1:8080:8080 -v "$PWD/data:/data" \
  -e ALLOWED_ORIGIN=https://your-site.example \
  github-trending-tracking:local
```

镜像只包含静态前端、Go 二进制和 HTTPS 根证书，运行时不需要 Node。整个 `/data` 挂载持久目录；删除重建应用容器不会删除历史。宿主 HTTPS 反向代理应把页面和 `/api/*` 一起转发到该应用。采集 Token 和备用 RSS 只能在运行时注入，不能用构建参数或 `VITE_*` 传入。

采集运行和运行时凭据见 [Go 采集说明](go-collection.md)。正式镜像发布、备份恢复和生产切流说明随对应任务完善；本地运行不能代替生产切换验收。
