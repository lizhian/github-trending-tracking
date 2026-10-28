# 加州服务器切流与回退

本手册对应 [#26](https://github.com/lizhian/github-trending-tracking/issues/26)。实际切换时刻、证据和未完成验收记录在该 Issue；文中的目标配置不代表生产验收已完成。允许直接停站，全程只保留一个活跃采集写入者。

## 目标与准备

服务器操作先读取 VPS 仓库的 `AGENTS.md`，复用以下既有资源：

| 项目 | 位置或配置 |
| --- | --- |
| SSH 主机 | `vps-tencent-ca` |
| Compose | `/root/docker-compose.yml`，服务 `github-trending-tracking`，共享网络 `root_default`，应用不发布宿主端口 |
| 数据 / 备份 | `/root/github-trending-tracking/data` / `backups`，容器内 `/data` / `/backups`，目录 `0700`、所有者 `10001:10001` |
| 秘密 | `/root/github-trending-tracking/secrets` 只读挂载 `/run/secrets`；文件 `github_token`、可选 `linuxdo_rss_fallback_url`，`root:10001`、`0440` |
| 运行环境 | `GITHUB_TOKEN_FILE=/run/secrets/github_token`、可选 `LINUXDO_RSS_FALLBACK_URL_FILE=/run/secrets/linuxdo_rss_fallback_url`、`BACKUP_DIR=/backups`、`BACKUP_TIME=03:00`、`ALLOWED_ORIGIN=https://github.lizhian.cc.cd` |
| 私有迁移证据 | `/root/github-trending-tracking/migration`，根目录 `0700`；保留源导出、审计、原 DNS/路由/Cron 及恢复报告 |
| HTTPS | `/root/caddy/Caddyfile` 挂载 `/etc/caddy/Caddyfile`，Compose 服务 `caddy`；复用现有 `site_defaults` / `cloudflare_dns` / `proxy_headers` 片段 |

完整业务镜像版本 `v1.0.0` 的已发布 index digest：

```sh
image='ghcr.io/lizhian/github-trending-tracking@sha256:88a4eec656e404b3439421527764b947c03c2e10a7381f9ec8ed155d50ce05e9'
```

部署前按 [镜像核验](image-release.md) 确认匿名拉取、双平台和版本/提交；实际使用的 digest 写入 Compose 及 Issue。保留当前 Compose、Caddy 和 Cloudflare 配置；本次共享配置备份目录为 `/root/.vps-config-backups/github-trending-20261028T152500Z`。检查服务器实际能访问 GitHub GraphQL 和既有趋势源，凭据仅从运行时文件读取。挂载、权限、上游或镜像不满足条件时先修复，再停旧调度。

## 停写、导入与验收

1. **停止旧 Cron。** 保存原 Worker 配置、Cron、页面绑定、API 路由和 DNS 记录，将 Cron 清空并查询控制面确认。Cron 更新可能需要最多 15 分钟传播；等待传播结束，再确认在途写入已结束。记录停写时刻和证据，保持新容器未启动。需要 Wrangler 登录/资源配置时只查 [旧流程](legacy/cloudflare-deployment.md)，不要运行会重新启用 Cron 的旧发布命令。

2. **取得冻结导出。** 本机复用已登录 Wrangler 与私有 `.wrangler/deploy.json`：

   ```sh
   umask 077
   npx wrangler d1 export DB --remote --config .wrangler/deploy.json --output /private/path/d1-frozen.sql
   scp /private/path/d1-frozen.sql vps-tencent-ca:/root/github-trending-tracking/migration/d1-frozen.sql
   ```

   导出必须包含 `d1_migrations` 的 0001/0002/0003 版本及全部物理历史；保留离线源文件。导出后旧调度继续关闭。

3. **导入新库。** 以下在服务器执行；目标 `tracking.sqlite` 必须不存在。临时容器使用 root 只为读取私有证据目录，导入后恢复应用目录所有权：

   ```sh
   docker compose -f /root/docker-compose.yml run --rm --no-deps --user 0:0 \
     -v /root/github-trending-tracking/migration:/migration \
     github-trending-tracking import --source /migration/d1-frozen.sql \
     --db /data/tracking.sqlite --report /migration/import-report.json --old-writer-stopped
   chown -R 10001:10001 /root/github-trending-tracking/data
   docker compose -f /root/docker-compose.yml run --rm --no-deps github-trending-tracking verify
   ```

   只有源/目标三表的 JSON 内容摘要、行数、类型/仓库/周期数量、全部点数/时间范围、入库时间与 effectiveAt/nextAt/blockedUntil 一致，完整性和外键通过，且 lease 例外有记录，才能继续。校验失败时保留文件、中止切换；新服务保持停止，再恢复旧 Cron。

4. **启动并同时切换页面/API。** 启动新应用，核对日志中的下一采集时段及每日备份结果：

   ```sh
   docker compose -f /root/docker-compose.yml up -d --no-deps github-trending-tracking
   docker compose -f /root/docker-compose.yml logs --tail 60 github-trending-tracking
   ```

   Caddy 为 `github.lizhian.cc.cd` 转发到 `github-trending-tracking:8080`，按现有片段规范配置后验证并重载：

   ```sh
   docker compose -f /root/docker-compose.yml exec -T caddy caddy validate --config /etc/caddy/Caddyfile
   docker compose -f /root/docker-compose.yml exec -T caddy caddy reload --config /etc/caddy/Caddyfile --address unix//run/caddy-admin.sock
   ```

   先核对源站 HTTPS 页面、静态资源、两类型列表与代表性详情，再把原域名 DNS 入口切到加州服务器，同时移除旧 `github.lizhian.cc.cd/api/*` Worker 路由。Pages 的旧项目和自定义域名资源保留七天；DNS 从 CNAME 改为 A 可能使 Pages 绑定变为 `deactivated`，回退时需重新验证。页面与 API 的实际请求都必须到新应用。验证同域请求返回正确 JSON，不以 HTTP 200 的 HTML 错误页算通过。

5. **验证真实运行。** 对公开域名检查类型切换、筛选排序分页、四窗口详情、URL 恢复及主题。首次 HTTP 读取已有历史只完成查询验收；等待严格晚于进程启动的下一应采时段，通过新的成功时间、Star 观测点与查询结果验收采集。停机缺测照实保留。确认服务器有新的完整备份；按 [恢复流程](go-runtime.md#停机恢复) 在空目录演练恢复、核对全量审计及 HTTP，并验收恢复后下一轮真实采集。停止当前应用后才能启动恢复副本的采集，保持单活。

## 回退

**Go 镜像回退：** 先确认旧 digest 兼容当前 SQLite schema/状态，记录当前版本并确认有效备份，按 [手动更新流程](image-release.md#手动启动与更新) 停旧容器、改 digest、重建单个服务。数据目录保持原挂载；需要数据库恢复时先保留故障目录，再恢复到空目录。

**七天内切回 Cloudflare：**

1. 停 Go 并生成一致备份，保留整个 SQLite 数据目录和新增观测：

   ```sh
   docker compose -f /root/docker-compose.yml stop github-trending-tracking
   docker compose -f /root/docker-compose.yml run --rm --no-deps github-trending-tracking backup
   ```

2. 从已保存配置恢复旧 Pages 页面入口、DNS CNAME 和 `/api/*` Worker 路由，保持旧 Cron 关闭、Go 容器停止。本次环境中，恢复 CNAME 后 Pages 自定义域名仍为 `deactivated`；在 Pages 控制台重试验证，或调用官方 [Retry custom domain validation](https://developers.cloudflare.com/api/resources/pages/subresources/projects/subresources/domains/methods/edit/)：`PATCH /accounts/{account_id}/pages/projects/github-trending-tracking/domains/github.lizhian.cc.cd`。等待绑定状态变为 `active`；重试后的短暂 `error` 也不算恢复完成。
3. 绑定恢复后，通过公开域名确认旧 HTML/静态资源、两类型列表和代表性详情均可用、API 确实读取 D1，并再次确认 Go 容器停止。全部通过后才恢复旧 Cron，等待传播并核对一次旧调度成功。记录唯一活跃库已变为 D1。旧站暂缺切换后的新 SQLite 观测；新库及备份继续保留，不做反向同步，不称为无损切回。

回退演练至少核对停止/启动顺序、实际路由和数据库去向；未实际执行的步骤在 Issue 标为待验收。

## 七天收尾

Issue 记录切流和观察期截止时刻（北京时间）、镜像 digest、唯一活跃写入库、源导出/备份位置、真实采集及恢复/回退证据。源导出、秘密文件和完整状态报告保持私有，公开记录只放安全摘要。

旧 Worker/Pages/D1 保留七天，Cron 关闭。#26 和 #21 保持打开，直到切流满七天、采集和恢复验收通过且没有未解决回退需要；不在本次执行中空等七天，也不自动创建监控任务。届时退役旧 Cron、API 路由及 Pages 生产入口，保留离线 D1 导出；Cloudflare DNS 可继续使用，资源物理删除另按维护者指令执行。以后常规维护使用 Go 镜像与 SQLite 备份。
