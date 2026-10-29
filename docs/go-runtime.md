# Go + SQLite 运行、备份与恢复

同一个 Go 进程提供前端和只读 `/api`，数据库路径默认 `data/tracking.sqlite`。服务启动要求已有有效 SQLite，不会悄悄创建空库，也不会重新初始化 `sampling_policy.effectiveAt`。

## 本地运行

```sh
npm ci
npm run typecheck
npm run build
mkdir -p data backups
# 仅用于全新、明确无历史数据的开发环境；已有生产数据使用备份恢复。
go run ./cmd/trending init --db data/tracking.sqlite
BACKUP_DIR="$PWD/backups" ALLOWED_ORIGIN=http://localhost:8080 go run ./cmd/trending serve
# 前端热更新：另开终端运行，代理 /api 到 Go 的 8080 端口。
npm run dev
```

`DATA_DIR`、`STATIC_DIR`、`LISTEN_ADDR` 可覆盖默认目录和监听地址；`--db`、`--static`、`--listen` 可明确指定。`ALLOWED_ORIGIN` 是允许的浏览器 Origin，热更新时设为 `http://localhost:5173`；不存在 Origin 的只读请求仍可查询。没有公开采集或管理接口。

检查：`go test ./...`、`npm run typecheck`、`npm run build`。测试使用真实临时 SQLite，通过公开调度、HTTP 和备份恢复接口验证业务结果。

SQLite 使用 foreign_keys、WAL 和 5 秒 busy timeout，启动时检查 schema、完整性、外键和可写性。数据目录通过内核文件锁限定一个活跃写入进程，异常退出自动释放。不要删除运行中的 `.writer.lock`；数据库、WAL 和 SHM 必须一起位于持久挂载目录。`migrations/` 是当前 Go 初始化 SQLite 使用的业务 schema，须随代码保留。

## 本地容器

```sh
docker build -t github-trending-tracking:local .
mkdir -p data backups
# 容器以 10001:10001 运行，Linux 宿主需给两个挂载目录相应权限。
# 仅全新开发库使用 init；已有历史使用一致备份恢复。
docker run --rm -v "$PWD/data:/data" github-trending-tracking:local init
docker run -d --name github-trending-tracking \
  -p 127.0.0.1:8080:8080 -v "$PWD/data:/data" -v "$PWD/backups:/backups" \
  -e BACKUP_DIR=/backups -e BACKUP_TIME=03:00 \
  -e ALLOWED_ORIGIN=https://your-site.example \
  github-trending-tracking:local
```

镜像只包含静态前端、Go 二进制和 HTTPS 根证书，运行时不需要 Node。整个 `/data` 挂载持久目录；重建容器继续读取原数据。HTTPS 入口把页面和 `/api/*` 一起转发到应用。采集 Token 和备用 RSS 仅在运行时提供，不能使用构建参数或 `VITE_*`。

采集和凭据配置见 [Go 采集说明](go-collection.md)，镜像发布、更新与兼容版本回退见 [版本镜像](image-release.md)。

## 每日同机备份

生产运行必须设置 `BACKUP_DIR`，指向已存在、可写、与整个活动数据目录分开的本地目录。容器应将它单独挂载到 `/backups`；活动库整个目录挂载到 `/data`。两者不可互相包含，禁止将备份放进活动库目录。Linux 宿主需同时为两个目录设置容器用户 `10001:10001` 的权限。

`BACKUP_TIME` 为固定北京时间 `HH:MM`，默认 `03:00`，不依赖宿主时区。应用每分钟独立检查最近一次每日备份时段；启动时若没有该时段的有效备份即执行一次，不受“采集等待下一轮”限制。重启根据已验证快照识别完成的每日备份，不重复生成。失败明确写入日志，下一分钟可重试。

快照通过 SQLite `VACUUM INTO` 包含一致的已提交 WAL 内容，不直接复制运行中主文件。新快照经 schema、完整性、外键和完整物理业务数据检查、同步并发布成功后，才删除时间已达 72 小时的旧备份；正常每日执行保留最近 3 天。失败保留已有可用副本。文件名为 `backup-UTC时间.sqlite`，文件权限为 `0600`。保留记录不使用外部服务、不增加业务状态表。

手动备份命令也是独占写入进程，先停应用，保持数据目录挂载，再执行：

```sh
# 本地：应用已停止，备份目录已存在。
BACKUP_DIR=/private/path/backups go run ./cmd/trending backup --db /private/path/data/tracking.sqlite
# 容器：先 docker stop github-trending-tracking，再以同一版本运行一次性命令。
docker run --rm -v /private/path/data:/data -v /private/path/backups:/backups \
  -e BACKUP_DIR=/backups github-trending-tracking:local backup
```

每日备份正常成功时，最近恢复点最多约损失 24 小时变更。同机备份不覆盖整机或磁盘丢失，不提供自动修复或 4 小时恢复承诺。监测应用日志中的 `daily backup validated` / `daily backup failed`，并在部署环境确认目录中存在新的有效快照。

## 停机恢复

1. 停止应用，确认旧进程退出；不与恢复过程同时运行采集。
2. 保留故障数据目录及其 `tracking.sqlite`、WAL、SHM 和已有备份，另建空的恢复目录。不要覆盖或删除唯一的故障副本。
3. 从已成功完成的独立 SQLite 快照恢复。恢复命令拒绝已有目标、活动写入目录和带 WAL/SHM/journal 的输入；检查快照并比较恢复前后的全部业务审计，成功才发布目标。

```sh
mkdir -p /private/path/restored-data
go run ./cmd/trending restore \
  --source /private/path/backups/backup-20261028T190000.000Z.sqlite \
  --db /private/path/restored-data/tracking.sqlite
go run ./cmd/trending verify --db /private/path/restored-data/tracking.sqlite \
  --report /private/path/restore-report.json
```

4. 核对报告中的项目/追踪周期数量、全部历史点及时间范围、`sampling_policy.effectiveAt`、pending 的 `nextAt` 和主机 `blockedUntil`。将应用的数据挂载切到恢复目录，备份仍挂独立目录，启动同一兼容版本。
5. 立即经同源列表与详情读取恢复数据；恢复后的采集仍等待严格晚于启动时间的下一应采时段，不补造停机历史。在下一轮核对新的真实采集成功后再完成恢复验收。

实现已通过真实 WAL 并发事务一致快照、停止应用后恢复到空目录、完整审计等价及 HTTP 历史查询验证；生产恢复与下一轮真实采集证据记录在 [#26](https://github.com/lizhian/github-trending-tracking/issues/26)。
