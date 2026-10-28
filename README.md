# GitHub 趋势追踪

[线上站点](https://github.lizhian.cc.cd/) · 页面和 API 共用该域名。

无需登录的中文只读趋势站。React / TypeScript / Vite 前端部署到 Cloudflare Pages；Worker 定时从八个趋势源发现项目、使用 GitHub GraphQL 批量获取 Star，D1 保存共享观测。

规格：[Issue #1](https://github.com/lizhian/github-trending-tracking/issues/1)、[多来源采集 #3](https://github.com/lizhian/github-trending-tracking/issues/3)、[开源观察界面 #4](https://github.com/lizhian/github-trending-tracking/issues/4)、[双栏工作台 #5](https://github.com/lizhian/github-trending-tracking/issues/5)、[顶栏与控件打磨 #6](https://github.com/lizhian/github-trending-tracking/issues/6)、[趋势标签与紧凑布局 #8](https://github.com/lizhian/github-trending-tracking/issues/8)。`prototype/` 是设计探索记录，正式页面位于 `src/`。

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
curl 'http://localhost:8787/__scheduled?cron=*+*+*+*+*'
curl 'http://localhost:8787/api/projects'
```

初次只会有“积累中”的真实快照。下一个应采时段再触发调度可比较净增量；不要把系统时间改到未来或将演示数据混入生产。单次最多解析 20 个新仓库，其余发现队列保存在 D1，后续调度继续处理。

## 检查

```bash
npm run typecheck
npm test
npm run build
npm run build:worker
npm run db:migrate
```

集成测试直接调用 Worker 的定时入口和公开查询，使用 Miniflare 的真实本地 D1，只控制上游响应与时间。涵盖多来源解析、逐源调度、详情队列、限流冷却、持久化/改名/重试、滚动窗口与缺测、筛选排序分页、保护期与容量清理及重新收录。测试数据库和开发数据库互相隔离。

`build:worker` 只打包，不部署；输出在被忽略的 `.wrangler/build`，不会混入 Pages 的 `dist`。

## 增量部署

更新现有站点使用 [Cloudflare 增量部署流程](docs/agents/deployment.md)，支持仅前端、仅 Worker、整站发布和本地 dry-run。

## 首次部署

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

部署后等待下一次每分钟定时任务，在站点及 `/api/projects` 的 `status` 检查发现/采集尝试时间、成功时间和失败数量；下一个应采时段检查详情是否出现新快照。查看 `wrangler tail --config .wrangler/deploy.json` 和 Cloudflare 控制台的 Cron / D1 指标排查平台级错误。上游错误只存安全状态码或固定诊断，不记录响应正文或凭据。

## 页面

正式页面采用暖纸色紧凑工作台。品牌和三项全站指标合并在固定顶栏，指标标签与数值同行，桌面约 52px、手机品牌与指标两条紧凑横排；桌面列表与详情独立滚动；点击项目任意一列均可选择，右侧详情回到顶部，列表位置不变。表格展示 24 小时、3 天、7 天、30 天四个窗口，点击表头排序；标签、筛选和表头保持在列表顶部。手机端上下布局，表格横向滚动、固定项目名称，选择后定位到下方详情，吸顶与回顶位置随实际顶栏高度调整。

语言和 Topics 使用可搜索筛选浮层，输入只过滤选项，选择后才提交；支持方向键、Enter、Escape、Tab 和点击外部关闭。搜索框使用完整外壳焦点高亮。详情拥有者小字、仓库名大字，优先同行显示，空间不足时自然换行；整块仍为 GitHub 新标签页链接。榜单指示线、列表及详情使用短暂切换动效，减少动态效果设置下直接展示。

标签为“github趋势”和“Linux.do开源趋势”；后者仅展示“暂未开放”，不请求接口，切回 GitHub 保留查询状态。表格五个数值列在绝对值大于 1000 时使用一位小数的 k 缩写，提示保留精确数值，排序使用原值。

列表和桌面详情为滚动条保留 16px 右侧安全区，列表另有底部横滚留白；表头按真实内容宽度同步。图表纵轴使用 k，必要时增加小数精度区分相近刻度，同值刻度不重复标注；精确值保留在提示中。历史加载时显示静态网格骨架，与真实图表共用标题、高度和反馈空间，避免加载完成后布局跳动。

URL 保存标签、筛选、排序、页码、稳定项目标识和详情窗口，支持刷新恢复与前进后退。默认详情窗口为 30 天，切换项目保留窗口。观测状态和实际区间放在可键盘展开的“观测详情”和“数据说明”中，保留过期、缺测与错误重试反馈。

顶部净增量由服务端汇总全站已知净增量，包含负值和部分观测，可比较项目数保留在 API 中；各项目实际区间可能不同，积累中不按零计算。

## 查询契约

- `GET /api/projects?q=&language=&topic=&sort=24&direction=desc&page=1`：名称/简介搜索忽略大小写，语言和一个 Topic 取交集。旧 `view` 参数已移除，传入时返回 400；旧页面链接会自动清除此参数。前端占位标签 `tab=linuxdo` 不传入 API。`sort` 支持 `name`、`stars`、`24`、`72`、`168`、`720`；`direction` 为 `asc` / `desc`。每页 100 项，超出页数归到最后一页。缺失增量始终置后，同值按仓库名称。返回所有已追踪项目的筛选选项、分页、概览和系统状态；`summary.net24` 为已知 24 小时净增量合计，`summary.comparable24` 为参与合计的项目数，无可比较项目时合计为 `null`。这些统计独立于当前筛选和分页；项目列表仅含窗口摘要。
- `GET /api/projects/:id/history?window=24`：`id` 是 GitHub GraphQL 稳定标识，窗口支持 24 / 72 / 168 / 720，返回实际 Star 快照、净增量、区间、观测状态、缺测段和新鲜度。不存在/已淘汰为 404。
- 非法参数 400、外部来源 403、写请求 405、数据库不可用 503。列表仅在 Worker 内部边缘缓存最多 10 分钟，对浏览器返回 `no-store`，防止域名浏览器 TTL 拉长数据陈旧时间；刷新不会启动采集。历史不缓存，后台异常不阻断有效数据读取。列表窗口摘要不含图表点，详情保留完整实际点。

窗口终点是项目最新成功快照，基准是窗口内最早的实际快照；查询和评分排除当前时刻 30 天前的数据。完整窗口按左边界后的首个应采时段及 10 分钟宽限判断；不足两点为“积累中”。真实负增量保留。跳过应采轮次时详情曲线断开，正常 2/4 小时间隔不算缺测；返回所有实际点，不插值。旧历史保留生效前的小时缺测与端点容差。

## 调度、存储与资源预算

Cron 每分钟唤醒调度器，Star 与 GitHub Trending 日榜按北京时间每日 01、05、09、11、13、15、17、19、21 点启动采样轮次（每天 9 轮）。趋势源只用于发现项目及上榜时间，不作为 Star 观测。

| 趋势源 | 地址 | 抓取时间 |
| --- | --- | --- |
| GitHub Trending 日榜 | https://rsshub-balancer.virworks.moe/github/trending/daily/any | 北京时间 01、05、09、11、13、15、17、19、21 点 |
| GitHub Trending 周榜 | https://rsshub-balancer.virworks.moe/github/trending/weekly/any | 北京时间每天 09:00 开始 |
| GitHub Trending 月榜 | https://rsshub-balancer.virworks.moe/github/trending/monthly/any | 北京时间每天 09:00 开始 |
| HelloGithub 全部 | https://rsshub-balancer.virworks.moe/hellogithub/home/all | 北京时间每天 09:00 开始 |
| HelloGithub 精选 | https://rsshub-balancer.virworks.moe/hellogithub/home/featured | 北京时间每天 09:00 开始 |
| OpenGithubs 日榜 | https://raw.githubusercontent.com/OpenGithubs/github-daily-rank/refs/heads/main/README.md | 北京时间每天 09:00 开始 |
| OpenGithubs 周榜 | https://raw.githubusercontent.com/OpenGithubs/github-weekly-rank/refs/heads/main/README.md | 北京时间每天 09:00 开始 |
| OpenGithubs 月榜 | https://raw.githubusercontent.com/OpenGithubs/github-monthly-rank/refs/heads/main/README.md | 北京时间每天 09:00 开始 |

首次启用立即抓取八个源，之后进入固定时段；漏跑后下一轮补抓一次，不补放历史周期。同一主机的请求串行执行，开始时间至少相隔 2 秒，包括 GitHub GraphQL。来源或详情抓取普通失败最早 30 分钟后重试；HTTP 429 或明确的限流响应使整个主机至少冷却 1 小时，并遵守更长的 `Retry-After` / GitHub 限流重置时间。GitHub GraphQL 上游失败也至少冷却 30 分钟；部分成功的仓库仍可保存。

GitHub RSS 只读取条目的 link/guid。HelloGithub 进入内容页，仅读取 `__NEXT_DATA__.props.pageProps.repo.url` 中的主项目，解析失败不扫描推荐区或页脚补凑。两个 RSS 的同一内容页共用当天成功结果，次日重抓；详情任务持久化，每轮最多请求 20 页，失败独立重试并保留原始发现时间。OpenGithubs 扫描当前 README 全文中的 GitHub 仓库链接（含子路径），归一为 owner/repo；仅在这些 README 中排除 OpenGithubs owner 的全部仓库（忽略大小写）。不遍历分页、历史文件或递归链接。

发现阶段最多使用 3 分钟，剩余任务留待下一轮；每次请求开始前预留完整超时预算。逐源执行状态、下次执行时间、详情队列和主机冷却保存在现有 `system_state`，无需数据库迁移。公开接口保留聚合 `status.discovery`，未恢复的来源或详情失败不会被其他来源的成功覆盖，错误包含来源标识。

每个计划轮次内，每分钟最多采样 100 个尚未成功的项目，由 D1 索引筛选并按最早尝试排序；500 项通常分 5 批完成，成功项目本轮不重复。平时一次 GraphQL 请求查询最多 100 个项目的稳定标识与 Star，元数据随采样每 24 小时刷新；需要刷新资料的项目另行成组，每批最多两次请求、合计最多 100 项。新增仓库每批最多 20 项，立即取得首个快照。失败遵守冷却继续追赶，部分仓库不可用时至少 30 分钟后重试；漏跑后只补最近已到的一轮，不补写过去观测。无采集尝试时不重复写采集状态。

同小时快照只允许更新的成功观测替换。失败不写零、不改变最后成功时间。调度租约避免正常重叠，每个追踪周期使用随机标识；采集和清理写回均核对周期，旧结果不能改写重新收录的项目。租约九分钟后可恢复，不足以获取上游资料的项目会重试。

小时快照按 UTC 日打包为 JSON，主键是 `(cycle, day)`，过期日有索引；项目行只保存四个窗口摘要与真实缺测区间，不生成列表图点。采样在 D1 中按小时 key 定位精确基准点，遇到缺测才搜索后续日记录；摘要越过 30 天边界时按需修正失效窗口。列表扫描约 500 行摘要并分页，详情才解析完整历史。物理清理只删完全过期的日记录，可多留约一天；所有查询与评分仍严格过滤 30 天。

北京时间每日 04:00 清理，漏跑后在下一次调度补做；固定当次候选和周期。目标 500 项，允许暂时超额，入库未满 24 小时绝不删除。错过下一应采时段及 10 分钟宽限、实际跨度不足 24 小时或任一窗口不足两点暂缓淘汰；正常 01 点快照在 04 点仍可评分。四个实际日均增长速度按 60% / 25% / 10% / 5% 加权，负增长只在评分中按零计。同分按最近上榜更早、24h 增量更小、稳定标识排序。淘汰级联删除历史，再上榜会开启新的追踪周期。

500 项规模、每天 9 轮的初步预算（无重试，需上线后按实际指标核实）：

| 操作 | 大致用量 |
| --- | --- |
| Star 采样 | 每天约 4500 次观测；按本地 09 点跨日批次保守折算约 18,000 写行/天，其他时段通常少于此批次 |
| 日记录索引与过期删除 | 每天约 500 个新日记录及约 500 个完全过期日删除；不逐小时改写边界日 |
| 趋势源与调度状态 | 稳态每天 16 次入口请求，另加 HelloGithub 详情、队列、租约与状态；均未计入采样折算 |
| 基准点与采样写回读取 | 本地 09 点批次折算约 45,000 读行/天，到期筛选与状态另计；缺测与边界回退会增加读取 |
| 公开查询 | 30 天边界刚滑动的本地冷列表数据加载为 11,501 读行（不含状态输出）；详情每项目最多 31 个日记录；筛选变体与边缘节点分别缓存 |
| 数据量 | 30 天约 135,000 个实际点、约 15,500 个日记录；本地测试数据库约 5.7 MB |

一批 100 项的本地 09 点稳态调度实测 18 次 D1 调用、406 写行，包含租约与状态。查询严格使用最近 30 天，物理按整日清理；策略版本与生效时间保存在现有 `system_state`，无需新增表或字段。本地 SQLite 计数未涵盖所有发现、重试、缺测和访问流量，不能视为免费运行承诺；生产还需验证完整调度 CPU、日读写和轮次覆盖率。历史生产测量见下，完整研究与复跑方式见 [免费资源研究](docs/research/free-tier-storage.md)。

参考：[D1 计费](https://developers.cloudflare.com/d1/platform/pricing/)、[D1 限制](https://developers.cloudflare.com/d1/platform/limits/)、[Workers 限制](https://developers.cloudflare.com/workers/platform/limits/)、[GitHub GraphQL 限流](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api)。实际部署后的 CPU、读写和首轮采集仍需部署环境验证。

## 首次生产验证

2026-10-28 已部署 Pages、Worker 和 D1，并绑定 `github.lizhian.cc.cd`。HTTPS 页面、同域列表查询及详情历史查询均已验证。北京时间 02:00 的首轮自动调度发现并成功采集 10 个真实项目，失败 0；本批次 CPU 17 ms、总耗时约 3.7 秒，D1 数据库约 64 KiB。

这是冷启动小批次的实测记录，不代表 1000 项或完整 30 天历史的资源用量。新项目从真实首个快照开始积累，页面初期显示“积累中”。D1 日读写统计有聚合延迟，稳定运行后再与上述预算比较。


## 合并发布验证

2026-10-28 已将紧凑工作台和免费运行第一阶段优化一起提交并发布。代码提交 `e9ec623`，发布验收中补充浏览器缓存修正 `2ca7750`；Pages 为 [fb47dd88](https://fb47dd88.github-trending-tracking.pages.dev)，Worker 版本为 `201ae0af-ee19-471a-a549-172701f4cc93`。生产已应用 `0002_free_tier.sql`，只新增元数据时间字段与到期索引，沿用现有资源。

19 项测试、类型检查、前后端构建和 Hallmark 渲染检查通过。线上核对资源哈希、项目联动、四窗口排序、独立滚动、移动吸顶、可搜索筛选、无请求的 Linux.do 占位页及旧 `view` 参数返回 400。列表仅在 Worker Cache API 内缓存 10 分钟，浏览器收到 `no-store`，避免域名浏览器缓存设置把数据陈旧时间延长为四小时。

北京时间 12:00 的新版本 Cron（`* * * * *`）实际成功采集 30 项、失败 0，同时更新 30 项元数据；scheduler 与 discovery 也有新的成功时间。Tail 记录 outcome 为 `ok`、CPU **34 ms**、wall time 约 7.76 秒。该轮包含发现与首次元数据刷新，当前实际追踪 100 项；成功执行不代表满足 Workers Free 的 10 ms 目标，更不能证明 1000 项、完整 30 天下稳定可用。后续优化仍按 [ADR 0002](docs/adr/0002-zero-charge-runtime.md) 的免费约束处理。发布结果见 [#9](https://github.com/lizhian/github-trending-tracking/issues/9)。
