# 版本镜像发布与手动更新

推送严格的 `vMAJOR.MINOR.PATCH` tag 会运行 `.github/workflows/release-image.yml`，使用标准 GitHub 托管 runner 构建并发布 `ghcr.io/lizhian/github-trending-tracking` 的 `linux/amd64` 和 `linux/arm64` 镜像。镜像包含 Go、SQLite 驱动、原前端及 HTTPS 根证书；运行不需要 Node。构建仅使用工作流自带的 `GITHUB_TOKEN`，权限为 `contents: read`、`packages: write`，不使用采集 Token，也不连接生产服务器。

```sh
# 在完整业务版本检查通过后发布；不要移动已部署版本的 tag。
git tag v1.0.0
git push origin v1.0.0
gh run list --workflow release-image.yml
```

工作流保存版本、提交和 OCI source/version/revision 标识；成功摘要记录镜像 index digest。它使用无登录配置验证公开清单，再分别启动两架构镜像，检查版本、静态资源和真实 SQLite API。只有 tag 推送触发构建，不自动部署服务器。

## 首次公开与产物核验

GHCR 首次 package 默认私有，公开源码仓库不保证镜像公开。首次发布后，打开 [package 设置](https://github.com/users/lizhian/packages/container/github-trending-tracking/settings)，将 visibility 改为 Public，再重跑因匿名拉取失败的工作流。工作流只用其 token 发布镜像，不为此新增个人发布 Token。

部署前取得成功摘要中的 digest，并在无 registry 登录配置下核验。以下 digest 是占位值，应替换成实际的完整 `sha256:…`：

```sh
image='ghcr.io/lizhian/github-trending-tracking@sha256:REPLACE_WITH_RELEASE_DIGEST'
anonymous_config="$(mktemp -d)"
docker --config "$anonymous_config" buildx imagetools inspect "$image"
docker --config "$anonymous_config" pull "$image"
rm -rf "$anonymous_config"
```

清单必须同时有 `linux/amd64` 和 `linux/arm64`。镜像版本标签便于检索，生产固定 index digest，Docker 按宿主架构选取对应平台。运行 `docker run --rm "$image" version` 可核对版本及提交；不要把构建成功当作已更新生产。

## 手动启动与更新

先按 [Go 运行与恢复](go-runtime.md) 准备有效 SQLite 并校验；既有生产目录不得用空库初始化。以下路径为示例，实际目录由部署环境确定。数据和备份必须是不同的本地持久目录，权限允许容器用户 `10001:10001` 写入。

运行时私密文件 `/opt/github-trending-tracking/runtime.env` 仅由维护者读写，包含 `GITHUB_TOKEN`、可选 `LINUXDO_RSS_FALLBACK_URL`、`ALLOWED_ORIGIN` 及备份配置。不要提交该文件，不用 `VITE_*`、构建参数或公开 issue 传递秘密。

```sh
image='ghcr.io/lizhian/github-trending-tracking@sha256:REPLACE_WITH_RELEASE_DIGEST'
docker pull "$image"
# 先记录当前 digest，并确认最近一致备份可用，再停止旧进程。
docker inspect github-trending-tracking --format '{{.Config.Image}}'
docker stop --time 240 github-trending-tracking
docker rm github-trending-tracking
docker run -d --name github-trending-tracking --restart unless-stopped \
  --stop-timeout 240 -p 127.0.0.1:8080:8080 \
  --mount type=bind,src=/opt/github-trending-tracking/data,dst=/data \
  --mount type=bind,src=/opt/github-trending-tracking/backups,dst=/backups \
  --env-file /opt/github-trending-tracking/runtime.env \
  -e BACKUP_DIR=/backups "$image"
curl --fail http://127.0.0.1:8080/api/projects
```

首次启动省略停止/删除旧容器的三条命令。HTTPS 入口将页面和 `/api/*` 一起转发到本机应用；不要暴露另一个采集实例。重建容器继续挂载原数据目录，HTTP 立即可读，采集统一等待严格晚于启动的下一应采时段。验证同域页面、列表与详情，并以下一轮新的成功采集判断后台是否正常。

回退 Go 镜像时，先确认旧版本兼容当前 SQLite schema 与状态，再按同样停止、重建步骤改用已记录的旧 digest；不能同时运行两个写入实例。若数据库也须恢复，按备份恢复流程停应用、保留故障文件，再恢复有效副本。不要让旧版以空库掩盖不兼容错误。

加州生产已纳入 `vps-tencent-ca` 的 `/root/docker-compose.yml`，服务名 `github-trending-tracking`。维护该服务器时先读取 VPS 仓库的 `AGENTS.md`，备份配置、修改该服务的镜像 digest，执行 `docker compose config --quiet` 后只更新该服务。沿用既有数据、备份、秘密文件挂载和 Caddy 入口；共享配置修改后同步回 VPS 仓库并核验 SHA-256。

本流程使用既有服务器、标准 GitHub runner 和 GHCR，不引入付费 runner 或新增自动按量扣费。

依据：[Docker 多平台 GitHub Actions](https://docs.docker.com/build/ci/github-actions/multi-platform/)、[GitHub package 可见性](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)、[公开 Container registry 的匿名访问](https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages)。
