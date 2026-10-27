# GitHub 趋势追踪

无需登录的中文只读趋势站。React / TypeScript / Vite 前端部署到 Cloudflare Pages；Worker 定时从唯一 RSS 趋势源发现项目、使用 GitHub GraphQL 批量获取 Star，D1 保存共享观测。

规格：[Issue #1](https://github.com/lizhian/github-trending-tracking/issues/1)。`prototype/` 是设计探索记录，正式页面位于 `src/`。

## 本地运行

需要 Node.js 22.12+（或 24+）、npm 和 GitHub Personal Access Token。公开仓库查询无需额外申请私有仓库权限；令牌必须能使用 GraphQL。

```bash
npm ci
cp .dev.vars.example .dev.vars
# 在 .dev.vars 中填写 GITHUB_TOKEN；该文件被 Git 忽略。
npm run db:migrate
npm run dev:worker
```

另开终端：

```bash
npm run dev
```

访问 `http://localhost:5173`。Vite 将 `/api` 代理至本地 Worker（8787）。首次采集在本地模拟定时事件，**该调试入口仅由 Wrangler 本地开发服务器提供**：

```bash
curl 'http://localhost:8787/__scheduled?cron=*/10+*+*+*+*'
curl 'http://localhost:8787/api/projects'
```

初次只会有“积累中”的真实快照。下一小时再触发调度可比较净增量；不要把系统时间改到未来或将演示数据混入生产。单次最多解析 20 个新仓库，其余发现队列保存在 D1，后续调度继续处理。

## 检查

```bash
npm run typecheck
npm test
npm run build
npm run build:worker
npm run db:migrate
```

集成测试直接调用 Worker 的定时入口和公开查询，使用 Miniflare 的真实本地 D1，只控制 RSS、GitHub 响应与时间。涵盖持久化/改名/重试、滚动窗口与缺测、筛选排序分页、保护期与容量清理及重新收录。测试数据库和开发数据库互相隔离。

`build:worker` 只打包，不部署；输出在被忽略的 `.wrangler/build`，不会混入 Pages 的 `dist`。

## 部署

实际资源创建、迁移及发布应在获得部署授权后进行。以下命令由维护者执行；仓库不含个人账号或数据库标识。设置 Cloudflare 凭据（`wrangler login`，或配置 `CLOUDFLARE_API_TOKEN` / `CLOUDFLARE_ACCOUNT_ID`）。

```bash
npx wrangler d1 create github-trending-tracking
export CLOUDFLARE_D1_DATABASE_ID='上一步返回的数据库 UUID'
export ALLOWED_ORIGIN='https://你的站点.pages.dev'
npm run configure:deploy
npx wrangler d1 migrations apply DB --remote --config .wrangler/deploy.json
npx wrangler secret put GITHUB_TOKEN --config .wrangler/deploy.json
npx wrangler deploy --config .wrangler/deploy.json
```

`secret put` 会提示输入 Token。Token 仅作为 Worker Secret，不能配置为 `VITE_*`、Pages 构建环境变量或公开 API 参数。`.wrangler/deploy.json` 由环境生成并被忽略；根配置的零 UUID 仅供本地开发。Pages 来源改变时，重新生成配置并部署 Worker。

前端只需要公开 API 地址：

```bash
export VITE_API_URL='https://github-trending-tracking.你的子域.workers.dev'
npm run build
# 首次创建时保持前端为独立 Pages 项目；避免新版 CLI 自动改走 Worker。
npx wrangler pages project create github-trending-tracking --production-branch main --force
npx wrangler pages deploy dist --project-name github-trending-tracking --branch main
```

如使用自定义域名承载页面和 API，可为 Worker 添加 `站点域名/api/*` 路由（区域选择该域名所属的 Cloudflare Zone），Pages 绑定同一个站点域名，并以空的 `VITE_API_URL` 构建前端。同域部署时生成配置需同时设置 `CLOUDFLARE_ZONE_NAME`，使后续 Worker 发布保留这条路由：

```bash
export ALLOWED_ORIGIN='https://你的站点域名'
export CLOUDFLARE_ZONE_NAME='你的 Cloudflare Zone 域名'
npm run configure:deploy
VITE_API_URL= npm run build
```

设置路由需要 Workers Routes 编辑权限；Wrangler 标准登录包含该权限。浏览器中已配置的路由也应同步到生成配置，避免后续发布丢失。同域方案的 `pages.dev` 默认域名仅提供静态页面，数据访问通过绑定的自定义域名进行。

Worker 仅接受 GET / OPTIONS，CORS 只允许配置的 Pages 来源（预览域名须另行配置，不使用 `*`）；没有公开采集或管理入口。CORS 是浏览器来源限制，公开数据仍可由非浏览器客户端读取。

部署后等待下一次十分钟定时任务，在站点及 `/api/projects` 的 `status` 检查发现/采集尝试时间、成功时间和失败数量；一小时后检查详情是否出现新快照。查看 `wrangler tail --config .wrangler/deploy.json` 和 Cloudflare 控制台的 Cron / D1 指标排查平台级错误。上游错误只存安全状态码或固定诊断，不记录响应正文或凭据。

## 查询契约

- `GET /api/projects?q=&language=&topic=&sort=24&direction=desc&page=1`：名称/简介搜索忽略大小写，语言和一个 Topic 取交集。`sort` 支持 `name`、`stars`、`24`、`72`、`168`、`720`；`direction` 为 `asc` / `desc`。每页 50 项，超出页数归到最后一页。缺失增量始终置后，同值按仓库名称。返回所有已追踪项目的筛选选项、分页、概览和系统状态；项目列表仅含精简折线。
- `GET /api/projects/:id/history?window=24`：`id` 是 GitHub GraphQL 稳定标识，窗口支持 24 / 72 / 168 / 720，返回实际小时快照、净增量、区间、观测状态、缺测段和新鲜度。不存在/已淘汰为 404。
- 非法参数 400、外部来源 403、写请求 405、数据库不可用 503。列表在边缘缓存最多 60 秒；刷新不会启动采集。历史不缓存，后台异常不阻断有效数据读取。

窗口终点是项目最新成功快照，基准是窗口内最早的实际快照；查询和评分排除当前时刻 30 天前的数据。完整窗口允许一小时端点偏差；不足两点为“积累中”。真实负增量保留。采样小时不连续时，详情和迷你图均断开；迷你图保留极值、端点和缺测边界，详情返回所有实际点，不插值。

## 调度、存储与资源预算

UTC 每 10 分钟运行一次。RSS 仅用于发现及上榜时间，不作为 Star 观测。已追踪项目每小时到期采样，每次最多处理 180 项、按最早尝试排序，分两组各 90 项请求 GitHub GraphQL；新增仓库按最多 20 项单独解析。可在一个小时内处理 1080 个既有项目，若软容量长期超额或上游持续失败，积压会明确表现为数据过期，后续调度继续追赶。

同小时快照只允许更新的成功观测替换。失败不写零、不改变最后成功时间。调度租约避免正常重叠，每个追踪周期使用随机标识；采集和清理写回均核对周期，旧结果不能改写重新收录的项目。租约九分钟后可恢复，不足以获取上游资料的项目会重试。

小时快照按 UTC 日打包为 JSON，主键是 `(cycle, day)`，过期日有索引；项目行保存四个窗口摘要。列表扫描约 1000 行摘要并分页，避免扫描全部小时历史；详情通过主键读取一个项目最多 31 个日记录。新鲜摘要额外保留最早四个实际点，保证 30 天边界滑动时基准准确；陈旧摘要越过保留边界时按索引重新读取实际历史。

北京时间每日 04:00 清理，漏跑后在下一次调度补做；固定当次候选和周期。目标 1000 项，入库未满 24 小时绝不删除。最新观测超过两小时、实际跨度不足 24 小时或任一窗口不足两点暂缓淘汰。四个实际日均增长速度按 60% / 25% / 10% / 5% 加权，负增长只在评分中按零计。同分按最近上榜更早、24h 增量更小、稳定标识排序。淘汰级联删除历史，再上榜会开启新的追踪周期。

1000 项规模的初步预算（需上线后按实际指标核实）：

| 操作 | 大致用量 |
| --- | --- |
| 小时采集 | 每天 24,000 次项目更新 + 24,000 次日记录写入/更新 |
| 快照索引与删除 | 每天约 1,000 次新日索引插入、约 1,000 个过期日级联/索引删除；边界日逐小时移除过期点 |
| RSS 与调度状态 | 144 次 RSS/天，榜单资料更新时间写入约 `144 × 榜单大小` 行；少量队列、租约与状态行 |
| 历史读取 | 每个小时采样至多 31 行，约 744,000 日记录行/天；清理候选另读最多约 31,000 行/天 |
| 公开查询 | 每个列表缓存未命中约 1,000 行；历史查询约 31 行；筛选变体和不同边缘节点分别缓存 |
| 数据量 | 约 720,000 个小时点，打包为约 31,000 日记录，随 30 天边界滚动删除 |

以上未把所有 SQLite 虚拟表扫描、JSON 处理 CPU、索引维护及重试折算成确定账单，不能视为免费运行承诺。尤其需要观察 D1 的 `rows_read` / `rows_written`、存储量，以及每次定时任务的 CPU / wall time。Cloudflare 当前免费 D1 每日额度为 500 万读行、10 万写行，单库 500 MB；Workers Free CPU 预算也需实测。已使用批量 D1 语句（一个 batch 计一次调用），单次正常采集的数据库调用数控制在 50 次以下，外部请求通常不超过 4 次。高流量、持续大量上榜、长期容量超额或冷数据查询可能超出免费额度。

参考：[D1 计费](https://developers.cloudflare.com/d1/platform/pricing/)、[D1 限制](https://developers.cloudflare.com/d1/platform/limits/)、[Workers 限制](https://developers.cloudflare.com/workers/platform/limits/)、[GitHub GraphQL 限流](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api)。实际部署后的 CPU、读写和首轮采集仍需部署环境验证。
