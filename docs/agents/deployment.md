# Cloudflare 增量部署

用于更新现有生产站。账号、D1、域名和 Pages 项目来自 `.env.deploy.local`；执行逻辑以 `scripts/deploy.mjs` 为准。初始化新资源见 [README 的首次部署](../../README.md#首次部署)。

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
