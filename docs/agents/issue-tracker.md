# Issue tracker: GitHub

本仓库的任务和规格存放在 lizhian/github-trending-tracking 的 GitHub Issues。
使用 gh CLI；运行前从 git remote 确认目标仓库。

## 操作约定

- 发布任务或规格：创建 GitHub issue。
- 获取任务：读取 issue 正文、标签和评论。
- 多行正文：写入临时文件，通过 --body-file 传入。
- 子任务：使用 GitHub 原生 sub-issue；不可用时，在子任务正文顶部记录 Part of #<parent>。
- 阻塞关系：使用 GitHub 原生 issue dependencies；不可用时，在正文顶部记录 Blocked by: #<number>。
- 所有阻塞任务关闭后，任务才可开始。
- 关闭任务：先评论说明结果，再关闭 issue。

## Pull requests as a triage surface

**PRs as a request surface: no.**

## Wayfinding

地图为带 wayfinder:map 标签的 issue，保存笔记、决策和待解决问题。
任务作为地图的子 issue，使用 wayfinder:<type> 标签。
选择地图顺序中没有未关闭阻塞项、尚未分配的第一个任务。
开始时分配给执行者；完成时评论结果、关闭任务，并将结论链接补充到地图。
