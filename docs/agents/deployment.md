# Go / SQLite / Docker 部署

生产使用单个 Go 应用容器和本地 SQLite。部署前读取对应流程：

- 构建、发布、手动更新和兼容版本回退：[版本镜像](../image-release.md)。使用明确 digest，发布流水线只构建，不连接服务器。
- 本地运行、SQLite 备份与恢复：[Go 运行说明](../go-runtime.md)。已有生产库使用备份恢复，空库 init 仅用于明确的新开发环境。

服务器操作先读取 `/Users/lizhian/Documents/lizhian-github/vps/AGENTS.md`，复用既有服务器、Compose 项目、HTTPS 入口及持久目录。当前生产为 `vps-tencent-ca`，Compose 服务 `github-trending-tracking`。运行时秘密通过受限文件或环境提供。

完成部署需验证同域页面、列表、详情和持久挂载；涉及采集的变更还需下一应采时段新的真实成功观测。回退使用兼容的 Go 镜像和 SQLite 备份，保持单一写入者。维护者已确认迁移结果并结束临时后备期，见 [ADR-0006](../adr/0006-go-only-operations.md)。
