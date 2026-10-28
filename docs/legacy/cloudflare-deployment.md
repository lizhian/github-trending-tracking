# 旧 Cloudflare 部署资料（归档）

仅用于 #26 迁移导出、七天后备期的紧急回退及旧行为比对。当前 Go/Docker 更新使用 [镜像流程](../image-release.md)，切流与回退使用 [生产运行手册](../production-cutover.md)。执行本文件中的部署命令会重新发布旧 Worker/Pages，并可能重新启用 Cron；先按回退手册停止 Go 写入进程。

保留 `worker/`、`wrangler.jsonc`、`scripts/deploy.mjs`、`scripts/configure-deploy.mjs`、Wrangler npm scripts 和 D1 测试供上述用途。`worker/contracts.ts` 仍是现有前端共享契约，`migrations/` 同时供 Go 初始化/导入使用。

旧流程说明如下：账号、D1、域名和 Pages 项目来自 `.env.deploy.local`；执行逻辑以 `scripts/deploy.mjs` 为准。初始化新资源见[首次部署](#首次部署)。

## 执行

1. **配置。** 本机已有 `.env.deploy.local`，直接复用。新检出先 `npm ci`，复制 `.env.deploy.example` 为 `.env.deploy.local`，填写现有资源标识；也可从本机 `.wrangler/deploy.json` 找回已生成的配置。标识缺失时，先用 `wrangler whoami --json`、`wrangler d1 list --json` 和 `wrangler pages project list --json` 查询现有资源。文件被 Git 忽略，凭据使用 Wrangler 登录态或环境中的 `CLOUDFLARE_API_TOKEN`，GitHub Token 保留在现有 Worker Secret。
2. **选择范围。** 按下表执行；未提交的工作区也可发布，Pages 会记录 dirty 状态。只要求修改代码时，完成本地检查；收到发布指令再运行发布命令。

   | 修改范围 | 命令 |
   | --- | --- |
   | `src/`、页面样式、`index.html`、Vite 配置 | `npm run deploy:pages` |
   | `worker/`、`migrations/`、Worker 配置 | `npm run deploy:worker` |
   | 共享契约、依赖、前后端联动 | `npm run deploy` |
   | 仅文档 | 无需发布 |

3. **发布。** 脚本预检账号和权限，确认 Pages 项目存在，完成类型检查及本地构建；涉及 Worker 时运行集成测试，再应用待执行 D1 迁移、发布 Worker，最后发布 Pages。生产迁移采用向后兼容的改动，保证新旧代码交接期间均可读取；涉及删除数据的迁移先单独审查和取得对应授权。
4. **验证。** 脚本检查 HTTPS 页面、同域列表及有项目时的详情历史；前端发布还核对线上 HTML 引用的资源哈希与本次构建一致。页面交互变化在浏览器核验一次。采集或调度变化再观察下一次 Cron 的 `status.collection` / `status.discovery` / `status.scheduler`，以新的成功时间和结果判断，不因已有数据能读取就认定后台成功。

本地演练（运行检查与构建，跳过 Cloudflare 登录检查、迁移、上传和线上请求）：

```bash
npm run deploy -- --dry-run
# 仅前端或 Worker 也可在对应命令后追加 -- --dry-run
```

发布后单独复查：

```bash
npm run deploy:verify
```

成功标准：选定组件的发布命令成功，线上页面与 API 契约验证通过；涉及采集的变更另有新一轮调度成功证据。报告站点、发布范围和实际验证结果。

## 失败时

- **登录或权限：** 使用脚本提示的 `wrangler login --scopes …` 命令补齐本项目权限。`workers_scripts:write` 负责脚本发布，`workers_routes:write` 负责同域 `/api/*` 路由，两者独立；仅有 `workers:write` 不够。浏览器已登录不代表 CLI 已登录。OAuth 同意页面按所用浏览器技能的人工协助规则处理。
- **Pages 项目不存在：** 回到首次部署流程核对账号及项目名。增量更新复用原 Pages 项目；新版 Wrangler 可能把不存在的 Pages 项目自动改走 Worker。`--force` 只用于确认后的首次 Pages 创建，日常发布使用脚本的存在性预检。
- **发布成功、验证失败：** 先单独运行 `deploy:verify`，避免重复上传。当地网络无法连接时，通过浏览器检查同一生产域名的页面资源、列表及详情请求；HTTP 200 的 HTML 错误页不算 API 成功。若仅 Pages 阶段失败，修正后只重跑 `deploy:pages`。
- **配置或 Secret：** 增量脚本每次从根 JSONC 和本地生产参数生成 `.wrangler/deploy.json`，保留 D1 绑定、Cron 和同域路由。生产 Worker 使用生成配置；根 `wrangler.jsonc` 的零数据库 UUID 是本地占位值。GitHub Token 使用 `wrangler secret put GITHUB_TOKEN --config .wrangler/deploy.json` 处理，常规发布沿用现有 Secret。

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
