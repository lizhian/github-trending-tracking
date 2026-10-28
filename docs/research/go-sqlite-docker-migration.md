# Go + SQLite + Docker 迁移调查与讨论

日期：2026-10-28。状态：现状调查与两轮讨论完成，用户已确认方案并要求发布 GitHub Issue 规格；ADR-0005 已接受，尚未开始业务实现或生产操作。

实施规格已发布为 [GitHub Issue #21](https://github.com/lizhian/github-trending-tracking/issues/21)，标记 `ready-for-agent`。

调查阶段读取仓库、既有 GitHub Issues 和官方技术文档，记录方案与决策；未读取生产数据库或秘密、未改写业务代码、未部署。生产实际数据量、已应用迁移和服务器环境仍须在实施准备时核实，不能从源码推定。调查时仓库为公开仓库，没有 GitHub Actions workflow，也没有未关闭的 issue。用户随后确认方案并要求按 `docs/agents/issue-tracker.md` 发布规格；本文保留事实依据与讨论决策，GitHub Issue 为实施规格。

## 已确定的目标与现有约束

- 后端改为 Go，数据库改为 SQLite；生产运行不依赖 Workers 或 D1，通过自有服务器 Docker 部署。
- 保持前端界面、交互和业务行为，优先兼容现有 HTTP 契约。前端只做必要的连接配置、共享契约位置和构建部署调整。
- 推送 `vMAJOR.MINOR.PATCH` tag 自动触发 GitHub Actions 构建 `linux/amd64`、`linux/arm64` 双平台公开镜像，发布至 `ghcr.io`；生产由维护者手动更新。用户已确认原文 `ghrc.io` 指 `ghcr.io`。
- ADR-0001 的保护期与可靠观测、ADR-0003 的采样计划和 ADR-0004 的独立追踪类型继续作为兼容基线；ADR-0003 的启动追赶行为按本轮 Q5 改为等待下一轮。
- 全量保留现有生产三表、全部存量历史和持久化调度状态；保留追踪周期、保护期和策略生效时间。
- 一个应用容器内由 Go 同时提供静态前端、现有 HTTP 查询和单个调度循环，SQLite 整个数据目录挂载到服务器本地磁盘，沿用当前域名；接受单机故障期间暂时不可用。
- ADR-0002 调整为允许使用既有服务器及明确批准的固定费用，继续禁止新增自动按量扣费；不再将项目描述为必然免费。额度不足时允许暂停。
- 重启后不恢复当前采集轮次，等待下一应采时段；接受由此产生的真实缺测和过期，不补造历史。
- 每日一次同机全量备份，保留3天，不设置异机副本或4小时恢复时限。切换允许直接停站，无30分钟窗口约束；旧站保留7天作为回退后备。

## 现状：数据与 HTTP 契约

| 范围 | 仓库事实与证据 |
| --- | --- |
| 业务表 | `projects`、`snapshot_days`、`system_state`。见 `migrations/0001_tracking.sql`，后续 `0002_free_tier.sql`、`0003_tracking_types.sql`。 |
| 项目标识 | `id=type:repo_id`；类型为 `github` / `linux.do`，`repo_id` 是 GitHub GraphQL 稳定 node ID；`cycle` 唯一标识本次追踪周期。0003 迁移专门保留 cycle，避免级联删历史。 |
| 项目字段 | name、description、language、topics、entered_at、listed_at、observed_at、attempted_at、error、stars、trends、metadata_at、type、repo_id。topics/trends 为 JSON 文本；时间为 Unix 毫秒。 |
| 历史 | `snapshot_days` 主键 `(cycle,day)`，外键 cycle 级联删除，WITHOUT ROWID；day 为 `floor(timestamp/86400000)`，hours 的键为 `floor(timestamp/3600000)`，值为 `[timestamp,stars]`。均是 UTC epoch 桶，不能改按北京时间分桶。见 `worker/store.ts:199`。 |
| 写入护栏 | 同小时仅较新观测可替换；项目写入受 id+cycle+观测时间保护；项目与快照通过 D1 batch 原子提交。见 `worker/store.ts:224`。 |
| 状态 | `sampling_policy`（version、effectiveAt）、`pending`、`discoverySources`、`upstream`、全局及分类型 discovery/collection/cleanup、scheduler、lease。包含待办、重试、主机冷却和 HelloGithub 解析进度，不能只迁可见项目。见 `worker/index.ts:34`、`:237`。 |
| 列表 | `GET /api/projects`，参数 q/language/topic/sort/direction/page/type，默认 github、24 小时净增量降序、每页100项。未知/重复参数拒绝，值最多200个 JS 字符；超页夹到最后页，空表 pages=1。见 `worker/api.ts:15`、`:109`。 |
| 筛选与排序 | q 匹配 name/description，language 精确匹配，topic 数组包含；空净增量始终置后，同值按英文 locale 名称排序，不能直接换成 Go/SQL 二进制排序。languages/topics/summary 属于当前类型全体项目，不受筛选和分页影响。见 `worker/api.ts:66`、`:81`。 |
| 历史查询 | `GET /api/projects/{编码id}/history?window=24\|72\|168\|720`，默认24，返回 points/trend/gaps/observedAt/stale。缺失项目404，参数错误400，内部错误503。见 `worker/api.ts:197`。 |
| HTTP 限制 | 仅 GET/OPTIONS，其他405；Origin 存在且非 ALLOWED_ORIGIN 时403，OPTIONS204；浏览器 no-store。保留现有 JSON 字段、中文错误、null、空数组和毫秒精度。见 `worker/api.ts:144`、`worker/contracts.ts`。 |
| 列表缓存 | 仅 Cloudflare caches.default，完整 URL 加内部版本键，最多600秒；当前类型无项目时不缓存，筛选无匹配但该类型有项目可缓存。详情无缓存，浏览器始终 no-store。无 KV/R2/Redis。见 `worker/api.ts:167`，修复背景 issue #20。 |

所有查询严格过滤最近30天实际观测；物理历史按完整 UTC 日删除，边界日可多留近一天。迁移原样保留全部物理记录，避免夹带清理。`sampling_policy.effectiveAt` 决定旧小时规则和新分时规则的分界，不能在 Go 首启重新生成。

## 现状：采集、恢复与淘汰

| 规则 | 当前行为与证据 |
| --- | --- |
| 时钟 | 固定 UTC+8 运算，不依赖宿主 TZ。北京时间每日01、05、09、11、13、15、17、19、21开始新 Star 轮次。平台每分钟 Cron。见 `worker/schedule.ts:3`、`wrangler.jsonc:6`。 |
| 发现 | GitHub Trending 日榜、Linux.do 随九时段；GitHub 周/月榜、HelloGithub all/featured、OpenGithubs 日/周/月榜每日09点。无状态初启立即发现。见 `worker/discovery.ts:10`、`:67`。 |
| 分批 | 每 tick 至多处理20条待收录身份解析，普通采样至多100个不同仓库，共享结果写回两类型；同轮成功仓库不重复，跨多轮只采最新轮，不补造历史。见 `worker/index.ts:87`、`:154`。 |
| 新收录 | 立即首采；另一类型有本轮成功观测时只复用该单个观测和原时间，入库时间/保护期从新收录开始，不复制旧历史。元数据随采样每24小时共享刷新。见 `worker/index.ts:132`、`:166`，`worker/store.ts:199`。 |
| 失败重试 | 来源、详情解析、待收录及已追踪仓库至少30分钟后重试，服从更长主机冷却；重新发现不清除冷却。见 `worker/discovery.ts:255`、`worker/index.ts:104`、`:155`。 |
| 上游保护 | 同主机请求开始时间至少间隔2秒；GitHub超时20秒、其他15秒，覆盖响应体；正文最多2,000,000字节。429、Retry-After、额度耗尽等至少冷却1小时，并取更长上游期限。普通 GitHub 请求/解析失败至少冷却30分钟。GraphQL 部分成功保留成功仓库。见 `worker/upstream.ts:43`、`:62`、`worker/github.ts:55`。 |
| 发现预算 | 整轮共用3分钟，含主备请求及 HelloGithub 详情；详情最多20次，未完成留待下一 tick。预算耗尽是延后，不是来源失败。见 `worker/discovery.ts:8`、`:187`、`:261`。 |
| Linux.do 主备 | 默认主入口，失败或主机冷却再试可配置备用；合法空 RSS 算成功；各自主机冷却独立，下次到期仍优先主入口。实际备用地址为秘密。见 ADR-0004、`worker/discovery.ts:153`。 |
| 缺测与过期 | 正常2/4小时间隔不算缺测；下一应采时刻后10分钟仍未成功才过期，切轮不会让旧观测重新变新鲜。旧策略以前保持小时缺测/2小时过期语义；完整窗口左端按对应策略判断。见 `worker/schedule.ts:47`、`:59`。 |
| 淘汰 | 各类型500软目标；最近北京时间04点的清理可追赶，无须逐日回放。入库未满24小时、过期、观测跨度不足24小时、任一窗口无可比净增量均暂缓淘汰，允许超额；error 标志本身不取消资格。见 `worker/retention.ts:14`、`:28`。 |
| 评分 | 各窗口真实观测日均增长按0.6/0.25/0.1/0.05加权；负增量仅评分时按0，页面仍展示负数。低分先删，同分按最近上榜更早、24小时增量更低、项目ID英文排序；删除仍检查周期和保护期。见 `worker/retention.ts:43`、`:69`。 |

恢复现状：数据库租约原子获取，9分钟到期，结束按 owner 删除；没有续租或过期写入隔离。成功提交后靠 observed_at 去重；请求成功但事务提交前崩溃会重发。主机冷却在最外层 finally 保存，硬终止可能丢失刚收到的冷却。旧租约可能让重启等待最多剩余9分钟。见 `worker/index.ts:237`。不能把现状描述成外部请求 exactly-once。

另一个需要显式保留的细节：普通观测时间当前取 tick 开始时间，而非 HTTP 完成时间；本次不夹带修正此语义，不把时间改成计划整点。

## 现状：前端、部署和依赖

- React/TypeScript/Vite；`src/main.tsx:9` 从 `worker/contracts.ts` 导入共享契约及 WINDOWS。后续移除 Worker 时保留或最小移动这份契约，不能把它当无用后端文件删掉。
- `src/main.tsx:50` 已支持空 VITE_API_URL 走同源 `/api`；请求 no-store，ID 已编码。`vite.config.ts` 仅需在未来把本地代理8787改到 Go 监听端口。
- 生产是 Pages 静态资源 + Worker 同域 `/api/*` 路由 + D1 + 每分钟 Cron。`scripts/deploy.mjs` 强制同源构建，并依赖 Wrangler 登录/权限、Cloudflare账号、区域、Pages项目和D1标识。
- GITHUB_TOKEN 仅在 Worker Secret；LINUXDO_RSS_FALLBACK_URL 为可选秘密。未来须从运行时环境或秘密文件提供，不进镜像、前端、仓库、日志。
- 后端除 Cloudflare 外仍依赖 GitHub GraphQL、RSS/榜单/详情上游、DNS及出站HTTPS。迁移主机可能遇到与 Cloudflare 不同的连通性或403，须实际验证，不承诺换主机自动解决。
- `package.json` 的 Worker 构建、本地 D1、测试依赖 Wrangler/Miniflare；部署脚本不能直接当 Docker 部署脚本使用。现有测试可作行为基线，不能假定能直接执行 Go。

## 已确认的完整方案

采用单个 Go 进程，提供现有只读 HTTP Interface、静态资源和单调度循环；一个应用容器、一个本地 SQLite 数据目录，服务器 HTTPS 入口转发到它。前端 Module 继续通过相同 HTTP seam 调用趋势追踪 Module；其 Implementation 内集中采集、趋势和淘汰规则。只在实际变化处放置数据库、外部请求和时钟 Adapter，不增加通用仓储层、多后端支持、队列或 Redis。

初期保留三表、字段和 JSON 日桶结构，启用外键，用 SQLite 事务替代 D1 batch。服务器本地磁盘挂载整个数据库目录，使用 WAL、busy timeout 和明确写事务管理；单个活跃调度执行者，不能通过增加容器副本扩容写入。写入进程独占数据目录，重复启动另一个写入进程应明确失败，进程内调度不重叠；不实现分布式租约续期或任务抢占。WAL 要求同机共享内存且同一时刻只有一个 writer，见 [SQLite WAL](https://www.sqlite.org/wal.html)。具体驱动及 WAL 配置在实施时基于双平台构建验证选取，不为当前讨论增加库选择问题。

按用户 Q5 的简化要求，采集启动门槛取严格晚于进程启动时间的下一个应采时段。所有启动采用同一规则，包括异常重启、升级重启及首次正式切换；启动后 HTTP 立即读取已有数据，发现和采集等到下一轮再开始。例：11:10 重启则13:00开始；21:10重启则次日01:00开始。恰在应采整点启动也等下一轮，避免为启动时间增加特殊分支。等待最多接近4小时，期间可出现真实缺测和观测过期。

到下一轮后恢复原有每分钟分批处理、至少30分钟失败重试及主机更长冷却；已持久化的 pending、nextAt、blockedUntil 等继续有效，不能因等待过一轮而清空。不回放停机期间轮次，不恢复当前轮次在途任务；同轮已提交观测仍去重，项目/快照事务及周期护栏保留。普通运行跨轮只处理最新轮次。每日04点清理及每日备份独立按原有/新定计划检查，不受采集启动门槛阻塞。无需精确恢复任务、逐请求日志或外部请求恰好一次保证。此为 ADR-0003 启动追赶行为的明确例外，其余采样、重试、缺测、过期、保护期及淘汰规则不变。

缓存沿用最多600秒、浏览器 no-store、空类型不缓存，以有界进程内缓存替代 caches.default；重启丢缓存无须恢复，不新增缓存基础设施。

按用户 Q6，每天生成一次同机全量 SQLite 一致快照，保留3天；备份目录在容器外、与活动数据库目录分开。采用 Online Backup 或 VACUUM INTO，不能对运行中的 WAL 数据库只 cp 主文件。完成新快照并校验成功后再清理过期备份，备份失败保留已有可用副本并输出明确失败信息。见 [SQLite Backup](https://www.sqlite.org/backup.html)。每日执行时间属于部署配置，不增加用户业务设置。正常每日备份成功时，可恢复至最近一次备份，最多丢失约24小时变更；不覆盖整机或磁盘丢失。恢复为停应用、保留故障文件、从有效备份恢复并校验、重新启动等待下一轮；不承诺自动修复或4小时内恢复。

发布采用 `vMAJOR.MINOR.PATCH` tag 触发构建 `linux/amd64`、`linux/arm64` 并推送 `ghcr.io/lizhian/github-trending-tracking` 的公开镜像，保存版本 tag 与提交标识，生产固定 digest。只自动构建发布镜像，生产更新由维护者执行。构建使用 workflow GITHUB_TOKEN，与采集 GitHub Token 分开。见 [Docker 双平台构建](https://docs.docker.com/build/ci/github-actions/multi-platform/)、[GitHub Container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。GHCR 新 package 默认私有，需明确设为公开，不能推定继承仓库可见性。

仓库已核实为 PUBLIC；官方当前说明公开库标准 GitHub 托管 runner 免费、GHCR 容器存储和带宽当前免费，但不能把未来计费政策或付费 runner 当作已获授权。见 [Actions 计费](https://docs.github.com/en/billing/concepts/product-billing/github-actions)、[Packages 计费](https://docs.github.com/en/billing/concepts/product-billing/github-packages)。

## 讨论决策汇总

首轮 Q1–Q4 已获用户逐项确认：

1. Q1：全量保留当前生产三表、全部存量历史及持久化调度状态。
2. Q2：单实例、前后端同容器同源、保留现域名；SQLite 挂载本地磁盘，接受单机故障期间不可用。
3. Q3：允许既有服务器及明确批准的固定费用，继续禁止新增自动按量扣费。
4. Q4：ghcr.io、linux/amd64 + linux/arm64、公开 vMAJOR.MINOR.PATCH 版本镜像；自动发布镜像，人工更新生产。

第二轮 Q5–Q8 已按用户回复确定：

- Q5：采用简单恢复逻辑，重启可跳过当前轮次，等待下一轮；不采用先前建议的启动立即追赶。
- Q6：每天一次同机全量备份，保留3天；不采用每小时备份、异机备份及4小时恢复目标。
- Q7：站点使用量少，可直接操作、允许停站；不要求30分钟维护窗口或零停机切换。
- Q8：旧 Worker/Pages/D1 保留7天作为回退后备，Cron 关闭；新系统启用后以SQLite为唯一写入库。若切回旧站，先停新调度，保留SQLite及一致备份，再启用旧调度；明确接受旧站暂缺切换后新观测，不开发自动反向同步。历史已迁移数据不得丢失，SQLite新增记录也不删除；重新进入当前生产历史需人工处理，不能称为无损切回。7天观察期结束、完成切流/采集/恢复验收后退役旧运行入口，D1导出离线保留，资源删除另行执行。

服务器 HTTPS 入口、实际数据/备份目录、每日备份执行时间和发布权限属于实施准备配置：由实施时查验环境后确定，不需要当前用户提供凭据；这些条件未满足时不能执行切流。现有 Cloudflare DNS 可继续使用，最终运行无需 Worker 路由、Cron、D1或Pages；DNS/CDN的保留不构成继续使用 Workers/D1。

## 已确认的迁移步骤与验收条件

允许直接停站：先停旧 Cron 并确认在途写入结束，再导出源库、导入新库并校验，启动新站验证同源页面与查询，切换域名入口后等待下一轮真实采集。新站正式启用后只运行新调度，旧调度保持关闭；不做双写或零停机编排。校验失败则中止切换并恢复旧站。官方支持 `wrangler d1 export --remote` 导出 SQL，导出期间会阻塞数据库请求，当前停站许可覆盖这一影响。见 [D1 导入导出](https://developers.cloudflare.com/d1/best-practices/import-export-data/)。仅导入业务 schema/数据并核对迁移版本，避免把 D1 内部管理表机械当作新应用表。旧 lease 仅在确认旧写入者已停止后有记录地清除；其余状态原样保留。域名切换时同时取消旧 `/api/*` Worker 路由，避免页面已迁而 API 仍读 D1。

- 数据：对源/目标三表按主键比较行数及内容摘要（lease 清除作为明确例外）；核对各类型数量、repo/cycle、原始点数和时间范围，以及 cycle/entered_at/effectiveAt/nextAt/blockedUntil；JSON 解析内容等价，不要求对象文本键序相同。执行 integrity_check 和 foreign_key_check。
- HTTP 与趋势：同一冻结数据和相同 now，对比两类型列表、代表性筛选/排序/分页、四窗口历史、状态和错误码；覆盖空/null、负净增量、缺测、过期、策略切换和共享仓库独立周期。尤其检查英文排序和 Go JSON 空数组。复用现有代表性夹具，不穷举所有参数组合。
- 调度：真实临时 SQLite 加受控上游/时钟，集中验证九时段、正常运行跨轮只追最新轮、失败重试/冷却、重启后当前轮不采及下一轮恢复、共享观测和04点淘汰；覆盖等待跨午夜及仍有效的更长冷却，证明旧周期不能写新周期、同轮成功不重复、第二个写入进程被拒绝。无需为每个内部函数另写测试。
- 持久与恢复：容器重建后数据仍在；每日生成完整一致备份，保留3天，失败不清除已有可用副本；执行一次从一致备份到空目录的恢复，核对数据及调度状态，并确认恢复启动等待下一轮。磁盘/权限/数据库错误须明确失败，不能悄悄改为空库运行。
- 前端与镜像：前端类型检查和构建；双架构镜像能启动并查询；现有页面在同源环境做一次关键交互核验，涵盖类型切换、筛选排序分页、详情/窗口、URL恢复和主题，无视觉改版。
- 切流：同域页面资源/列表/历史通过，加上一次新的真实采集成功证据；回退演练必须同时检查路由、活跃调度唯一性和数据库去向。不能只凭已有数据可读认定成功。

回退区分两类：同一服务器回退旧 Go 镜像时要求数据库仍兼容；七天后备期内切回 Workers + D1 时，按 Q8 接受旧站缺少新 SQLite 观测，保留新库和一致备份但不自动反向导入。域名解析收敛期间也必须保持单一采集写入者，恢复旧站须恢复其页面与 API 路由。七天后退役旧入口后，常规恢复使用 Go 版本镜像、SQLite备份及离线迁移导出，不承诺继续随时切回 Cloudflare。

Q1–Q8 已确定，无待用户选择的架构分支。“重启等待下一轮”统一用于所有启动；用户已要求将完整方案整理为规格并发布，ADR-0005 据此接受。本轮只发布规格及定稿文档，不启动业务改写、生产迁移或部署；后续实施遵守用户届时指令及部署约定。
