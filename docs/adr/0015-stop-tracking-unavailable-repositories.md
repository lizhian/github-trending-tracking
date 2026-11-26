---
status: accepted
---

# 仓库确认不可访问超过 72 小时后结束追踪

首次确认仓库不可访问后，沿用 [ADR-0013](0013-current-time-windows-and-unavailable-repositories.md) 的持平计算和正常容量淘汰规则。超过 72 小时后，若常规采样再次通过带 Token 查询明确查无仓库，且匿名复核返回 404，则一起删除该仓库在 GitHub 和 Linux.do 的追踪项目及其 Star 快照，结束主动采样。刚满 72 小时或复核遇到临时故障时保留项目。

删除前恢复访问会清除不可访问时间，延续原追踪周期。删除后，趋势源重新发现或用户投稿仍可按对应类型的规则收录，成功后开启新追踪周期。决策见 [#56](https://github.com/lizhian/github-trending-tracking/issues/56)。
