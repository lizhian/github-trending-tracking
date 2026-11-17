# AI 项目总结与标签

设计已确认，进入实现。

## 已确认

- 主列表简介优先显示一句话中文 AI 项目总结，项目名称右侧展示 AI 项目标签。
- 以 GitHub 识别的主 README 为生成依据；确认缺失时，依据仓库简介同时生成总结和标签。两者都没有时不调用模型，显示“暂无项目介绍”，标签留空。
- 生成 3～5 个自由分类的简短中文标签，技术专名保留英文；不建立固定分类表，本次不增加 AI 标签筛选。
- 在元数据刷新时生成；依据内容签名判断是否需要更新，无 README 时检查简介变化。模型或提示词改变不触发自动重生成。
- 仅在成功生成后提交对应签名。模型超时、限流或格式错误不阻断元数据和 Star 更新；保留上次成功结果，首次失败回退原简介，下次元数据刷新重试。
- 端点 `https://opencode.ai/inference/go/openai/v1`、模型 DeepSeek V4.1 Flash 1017（API ID `deepseek-v4.1-flash`）仅授权少量真实测试；密钥不写入仓库。
- 生产通过环境变量配置端点、模型和密钥；未配置时展示原简介。上线启用前明确费用边界，继续遵守 ADR-0002。

## 补充确认

- 存量项目随下一次元数据刷新逐步补齐，同仓库跨追踪类型共享结果。
- README 获取失败保留结果，下次刷新重试；仅确认缺失时回退简介。
- 元数据刷新提交后台任务，生成不持有采样和投稿锁，同仓库串行去重。
- 标签保持单行，超出部分隐藏，以“+N”展示隐藏数量，悬停显示隐藏标签。

## 配置与运行

- `AI_BASE_URL`：OpenAI Chat Completions 兼容服务的基础地址（包含 `/v1`，不包含 `/chat/completions`）。
- `AI_MODEL`：模型 ID。
- `AI_API_KEY` 或 `AI_API_KEY_FILE`：密钥，沿用服务现有 secret 文件读取方式。
- 三项均配置后启用；清洗维护模式下不运行。生成任务串行执行，等待中的任务持久化，可在重启后继续。
- README 完整内容计算 SHA-256 签名，模型输入最多使用前 24,000 个字符；主 README 获取上限 2 MiB。简介回退与 README 使用不同签名空间。
- 列表沿用现有最多 10 分钟刷新缓存，生成完成后的结果随缓存刷新展示。
- 不覆盖 GitHub 原始简介和 Topics，不改变原有 Topics 筛选语义。

## 验证结果

- `go test ./...`、TypeScript 类型检查和前端生产构建通过。
- 模拟接口覆盖完整 README 签名复用、生成失败保留旧结果且不提交新签名、失败后重试、README 获取失败不调用模型、简介回退及空内容处理。
- 使用指定端点完成真实主 README → 模型 → 持久结果的流程验证，再次处理相同 README 命中缓存。
- 模型目录将 DeepSeek V4.1 Flash 1017 的 API ID 定义为 `deepseek-v4.1-flash`，日期后缀仅在 canonical ID 中；请求必须使用 API ID。参考[模型目录](https://opencode.ai/inference/go/v1/models)。
- 服务端要求客户端 User-Agent 和稳定的 `x-opencode-session`，请求已补齐这两个头。参考[服务说明](https://opencode.ai/docs/console/go/#where-can-i-use-it)。
- 浏览器局部验证：窄容器显示“+N”，宽容器展示全部标签，悬停能显示隐藏标签。

测试生成示例：一个无需登录的中文只读趋势追踪站，从多个来源采集项目并记录真实 Star 变化。

任务：[GitHub Issue #46](https://github.com/lizhian/github-trending-tracking/issues/46)。本次未发布或部署，未在仓库保存密钥。
