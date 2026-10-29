---
status: accepted
---

# 结束临时后备期，统一使用 Go、SQLite 和 Docker

2026-10-29，维护者确认新线上站点运行正常，要求停止旧 Cloudflare 部署并关闭 #26、#21，提前结束 ADR-0005 中的七天后备安排。后续运行、版本发布、备份恢复和兼容版本回退统一使用 Go、SQLite 和 Docker，减少两套运行代码、测试和部署说明的维护成本。

移除旧 Worker、Wrangler 发布配置和旧运行时测试，前端契约移入 `shared/`；继续保留当前 SQLite 业务迁移、Go 测试及 SQLite 一致备份。旧运行入口退役结果记录在 [#26](https://github.com/lizhian/github-trending-tracking/issues/26)。Cloudflare DNS 继续承载现有域名。

维护者进一步明确删除本项目的 Worker、Pages 和 D1 云端资源，以及本机和服务器上的 D1 归档。当前 SQLite 已保存生产数据并具备一致备份，移除仅用于 D1 归档的导入命令和测试，继续保留备份恢复所需的通用审计。恢复使用 SQLite 一致备份及兼容 Go 镜像，Cloudflare 仅继续承担现有域名的 DNS。
