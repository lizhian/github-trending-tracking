# GitHub 趋势追踪

[线上站点](https://github.lizhian.cc.cd/)

无需登录的中文只读趋势站，从 GitHub Trending、HelloGithub 和 OpenGithubs 发现项目，持续记录真实 Star 变化。

- 比较 24 小时、3 天、7 天和 30 天的 Star 净增量，查看实际观测曲线。
- 按名称、简介、语言和 Topics 搜索筛选，支持排序、分页和 URL 状态恢复。
- 桌面列表与详情联动，支持移动端、浅深色主题及键盘操作。

前端使用 React / TypeScript / Vite，部署于 Cloudflare Pages；Worker 负责采集与只读 API，D1 保存所有访客共享的观测数据。

## 本地运行

需要 Node.js 22.12+（或 24+）、npm，以及可查询 GitHub GraphQL 的 Personal Access Token。

```bash
npm ci
cp .dev.vars.example .dev.vars
# 在 .dev.vars 中填写 GITHUB_TOKEN。
npm run db:migrate
npm run dev:worker
```

另开终端运行 `npm run dev`，访问 `http://localhost:5173`。Vite 将 `/api` 代理至本地 Worker。

首次采集通过 Wrangler 本地调试入口触发：

```bash
curl 'http://localhost:8787/__scheduled?cron=*+*+*+*+*'
```

新项目从首个真实快照开始积累；下一个应采时段再触发调度后，才能比较净增量。GitHub Token 仅用于 Worker，本地配置由 Git 忽略。

## 数据说明

查询保留最近 30 天的实际观测，窗口以项目最新成功快照为终点。负净增量原样展示，缺测不补零或插值；不足两个有效快照显示“积累中”。项目按计划分批采样，页面刷新只读取已有数据。

目标保留 500 项，24 小时保护期和可靠观测条件优先，因此允许暂时超额。列表最多有 10 分钟缓存延迟。具体采样时段、窗口规则和容量取舍见[架构决策](docs/adr/)。

## 检查

```bash
npm run typecheck
npm test
npm run build
npm run build:worker
```

集成测试使用隔离的真实本地 D1。`build:worker` 仅打包，不部署。

## 部署与维护

- 创建新资源：[首次部署](docs/agents/deployment.md#首次部署)。
- 更新已有站点、验证或排查发布失败：[Cloudflare 部署流程](docs/agents/deployment.md)。
- 理解领域概念：[术语表](GLOSSARY.md)。
- 评估免费运行条件：[资源预算与生产测量](docs/research/free-tier-storage.md)。本地验证不代表生产 CPU 已达标。
- 查阅实施规格和发布结果：[GitHub Issues](https://github.com/lizhian/github-trending-tracking/issues)。

## 相关链接
[Linux.do](https://linux.do/): 连接开发者与 AI 爱好者的社区。