# GitHub 趋势追踪 · UI 原型

本原型用于比较三种页面结构，所有数值、趋势和状态均为演示数据，不调用外部数据源、不持久保存操作。尚未选定最终设计。

在仓库根目录运行 `npm run prototype`，打开 http://127.0.0.1:4173/prototype/trends/ 。无需安装依赖。

- `?variant=A`：精简表格；聚焦多周期数据比较。
- `?variant=B`：探索工作台；固定筛选侧栏与紧凑内容区。
- `?variant=C`：热点观察刊；先看热点速览，再查看完整榜单。

底部工具条或左右方向键切换设计。搜索框内不拦截方向键。三个方向共用搜索、语言/Topics 筛选、排序、50 项分页、手风琴、图表周期切换、浅深色主题。主题与筛选只保留在当前页面内存中。

使用 Node.js 启动。可通过 `PROTOTYPE_PORT` 指定端口。`NODE_ENV=production` 时隐藏原型切换工具条。此原型不作为正式产品直接部署。

设计依据：ui-ux-pro-max 检索匹配的 Data-Dense Dashboard 风格，采用中性色表面、单一强调色、增长语义色、对齐数值和可见焦点。自动设计系统给出的营销落地页结构不适用于本产品，未采用。

对应实施规格：https://github.com/lizhian/github-trending-tracking/issues/1 。原型代码与后续真实采集、D1 和 Cloudflare 部署独立。
