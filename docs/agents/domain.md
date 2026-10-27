# Domain docs

本仓库采用 single-context 布局：
- 根目录 GLOSSARY.md：领域术语。
- docs/adr/：架构决策记录。

## 探索代码前

读取 GLOSSARY.md 和与当前工作相关的 ADR。
文件不存在时直接继续；由 domain-modeling 在术语或决策确定后按需创建。

## 使用规则

在 issue、设计、代码和测试中使用术语表定义的名称。
遇到缺失术语时，先确认是否属于真实的领域概念，再记录供 domain-modeling 处理。
方案与现有 ADR 冲突时，明确指出对应 ADR 和重新讨论的理由。
