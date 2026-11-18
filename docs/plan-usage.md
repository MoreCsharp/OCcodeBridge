# 套餐用量

用量窗口由凭据所属**厂商**返回：插件把各厂商 `FetchUsage` 的结果转成统一窗口列表，供「凭据管理」卡片和 CPA 配额接口共用。插件不按价格推算金额。

## 数据来源

| 厂商 | 用量接口 | 窗口种类 | 额度暂停依据 |
| --- | --- | --- | --- |
| OpenCode Go | `GET https://opencode.ai/zen/go/v1/usage`（Bearer API Key） | 5 小时、每周、每月 | 该接口返回的已用百分比或已用/上限 |
| CommandCode | `GET https://api.commandcode.ai/alpha/whoami?limits=1`、`/alpha/billing/credits`、`/alpha/billing/subscriptions`（带 Bearer Key，按组织加 `orgId`） | 5 小时、每周、每天、每月 | `windowLimits` 的 `used`/`cap` 与月度额度池 |

用量查询使用凭据的 API Key，走插件的出口层与超时设置；上游 `401`/`403` 以该凭据的查询状态返回，不会触发 CPA 管理密钥登录弹窗。

### OpenCode Go

`FetchUsage` 先读 API-key 形状 `{"usage":{"rolling","weekly","monthly"}}`，没有再读旧控制台形状 `{"access":{"meters":{"fiveHour","week","month"},"endsAt"}}`。每个窗口接受 `percent`/`percentUsed`/`usedPercent`、`used`/`cap`，控制台形状的 `usedMicroCents`/`limitMicroCents` 会换算成美元；缺失字段保持未知，不补 0。

周窗口在上游没有给出重置时间时，按「下一个周一 00:00 UTC」推算，并在卡片标注「按规则推算」。`滚动` 窗口对应 CPA 的 `5h`，`weekly` 对应 `7d`，`monthly` 对应月初到月末的日历月。

### CommandCode

一次查询聚合三个接口：`whoami` 决定组织范围，`credits` 提供滚动窗口与付费余额，`subscriptions` 提供套餐和本期起止时间；`whoami` 或 `subscriptions` 失败只丢失它们负责的字段，`credits` 失败则整个查询失败，`401` 直接返回鉴权失败。

`windowLimits` 只在 `limited` 为真时生成 5 小时/每周/每天窗口，`used`/`cap` 单位为 credits（约等于美元）。每月窗口按 CLI 的 `projectUsageView` 折算：本月套餐额度 + 购买额度 + 赠送额度构成上限，剩余额度就是三者之和，已经用掉的 `alpha/usage/summary` 尚未查询，因此月窗口在未持有有效订阅时只反映当前余额。付费余额（购买 + 赠送）作为「额外额度」显示。

## 管理页

「凭据管理 → 编辑/展开」卡片按厂商显示厂商名、套餐名与本期截止时间，以及该厂商实际报告的窗口：5 小时、每周、每天、每月进度条、已用/上限金额和重置时间。没有数据的窗口显示 `—` 与「重置时间 —」，不显示为 0%；重置时间按浏览器本地时区显示。缺失整段数据时行为与旧版一致：保留上次成功值并标注过期。

| 状态徽章 | 含义 | 清除方式 |
| --- | --- | --- |
| 人工停用 | 操作者关闭了该凭据 | 手动开启 |
| 额度耗尽 · 自动暂停 | 某个窗口达到 100%，暂停到该窗口重置时间（多个窗口取最晚） | 窗口重新低于 100% 后由用量轮询恢复 |
| 套餐不支持 API | 上游明确表示该套餐没有 API 权限（持久化，重启保留） | 升级套餐后点「重新检测」，或更换 API Key |
| 查询失败 | 本次查询失败，卡片保留旧值与过期提示 | 下次查询成功 |

ZDR 开关只对声明支持零数据保留的厂商（当前为 CommandCode）显示。

凭据按名称排序，每页 12 个，可搜索名称或 ID。后台按凭据约每分钟采样用量，不依赖页面是否打开；同一账号的多个 Key 完成身份识别后合并查询并共享样本。最多同时查询 3 个凭据，结果缓存 60 秒（失败 30 秒），手动刷新保留 10 秒最小间隔，后台遇鉴权失败、限流或套餐停用时退避 5 分钟。

## 暂停与恢复

### 窗口额度保护（`pause_on_window_limit`）

新装和旧配置默认开启。开启时，用量采样发现任一 5 小时/每周/每天/每月窗口 `UsedPercent >= 100` 或 `Used >= Cap`（`Cap > 0`）就暂停该账号的调度，直到窗口重置时间（多个窗口取最晚）。这是防止 CommandCode 在窗口用尽后自动扣用 extra credits、OpenCode Extra Usage 扣余额的主要手段。

关闭后，用量轮询不再主动暂停，但上游返回窗口超限 429（`ClassifyLimitError` 判定为窗口限制）时仍按额度暂停处理。配置项写在设置文件里，管理页「配置概览 → 窗口额度保护」可开关：

```json
{"pause_on_window_limit": true}
```

### 没有重置时间时

窗口超限但没有重置时间（上游 429 报错未给时间，或用量接口缺字段）时，账号仍按现有用量轮询恢复：下一次采样显示该窗口低于 100% 即恢复调度。同时设置 10 分钟兜底复查间隔，避免旧样本的缓存有效期让暂停一直等不到新数据；兜底只保证重新查询，不会在窗口仍满载时恢复。

### 三种状态互不覆盖

| 状态 | 归属 | 对其他状态的影响 |
| --- | --- | --- |
| 人工停用（凭据 `disabled`） | 操作者 | 额度暂停、恢复都不修改它 |
| 额度暂停（窗口超限） | 用量轮询 / 上游 429 | 恢复只清除额度状态 |
| 套餐不支持 API（永久） | 上游明确拒绝 | 用量轮询无法清除；需重新检测或更换 Key |

人工停用和额度暂停都不会停止用量查询；额度恢复不会自动启用人工停用的凭据。更换 API Key 后旧额度状态（含永久状态）失效，旧请求的结果也不会覆盖新 Key。

### 重新检测

「凭据管理」中处于「套餐不支持 API」的凭据提供「重新检测」，对应接口 `POST /v0/management/occodebridge/credentials/recheck`，请求体 `{"id":"<凭据ID>"}`。插件执行一次强制用量查询：查询成功即清除永久状态并恢复调度；仍失败则返回 `cleared:false`、状态和原因，状态保持不变。

## CPA 标准配额接口

插件通过 CPA JSON ABI 声明 `quota_provider` 能力，并提供 `quota.identifier`、`quota.describe`、`quota.fetch` 和 `quota.reset`。外部管理客户端可调用 `POST /v8/management/plugins/occodebridge/quota`，请求体 `{"auth_index":"<auth_index>"}`；GET 路由支持 `?auth_index=<auth_index>`。`auth_index` 由 CPA 解析到对应账号，插件不会回退到其他账号。

响应使用 CPA 标准的 `subscription` 和 `groups[].buckets[]`：

| 内部窗口 | `window` | 说明 |
| --- | --- | --- |
| `five_hour` | `5h` | 滚动 5 小时 |
| `weekly` | `7d` | 每周 |
| `daily` | `daily` | 每日窗口 |
| `monthly` | `monthly` | 日历月 |

CPA 契约（`sdk/pluginapi/types.go`）把 `window` 作为开放字符串，示例值包含 `5h`、`7d`、`daily`、`monthly`。5 小时与每周使用短窗口写法，日/月窗口没有专用短写法，因此保留 `daily`、`monthly` 语义，不硬塞成固定时长窗口。

`remainingFraction` 为 0–1 的剩余比例：优先用已用/上限（`Used/Cap`）算出，其次用已用百分比；两者都没有的窗口不生成额度条，不推断为 0% 或额度耗尽。`resetTime` 使用上游重置时间。`subscription.plan` 取用量样本的套餐名称（OpenCode Go 的 `planName`/`plan.name`，CommandCode 计划表映射后的名称）。标准接口只输出厂商显示名、套餐名和已报告窗口，不输出 API Key、账号/订阅标识或余额金额。

标准接口复用页面与后台的缓存、合并、代理和额度保护逻辑，主动查询保留 10 秒最小间隔。手动停用与额度暂停的账号仍可查询。上游查询失败返回错误（鉴权 401、套餐不支持 403、限流 429、超时 504、其他 502），避免把旧数据当作当前配额。

上游不提供额度重置操作，`quota.describe` 声明 `supports_reset: false`，`quota.reset` 返回 `success: false`，不清除 CPA 冷却、不修改启停或额度状态。

## 日志中的额度

上游在响应帧里报告的额度字段会记入日志：`allowance` 保存原样对象，`allowance_raw` 保存读取该值的原始 JSON 片段（超过 2 KiB 截断并加 `…`）。日志详情在存在时显示「计入额度」，内容为上游原样数值/JSON；没有则不显示该行。

## 未核实项

- OpenCode Go `GET /zen/go/v1/usage` 的真实响应结构、`percent` 单位与 `resetsAt` 格式，以及 429 窗口超限的 `GoUsageLimitError` 字段名，均尚未用真实 Key 核实；当前解析为容错推测，缺字段时退化为未知或按规则推算。
- 周窗口「下一个周一 00:00 UTC」来自官方文档，`derived` 只是本插件对推算值的标注，不是上游字段。
- CommandCode 的 `alpha/*` 接口为 CLI 使用的非公开路径，无公开稳定性保证；月窗口是折算值，`alpha/usage/summary` 尚未查询，月度已用可能偏低。
- 额度暂停只阻止插件继续发送请求，实际是否会扣用额外额度由上游结算决定；关闭 `pause_on_window_limit` 时超窗口消费属于预期行为。

## 接口与兼容性

- 插件管理接口：`GET /v0/management/occodebridge/credentials/usage?id=<凭据ID>`（手动刷新加 `refresh=1`），与 `POST /v0/management/occodebridge/credentials/recheck`，均由 CPA 管理鉴权保护。
- 代理池关闭且凭据未指定独立代理时，用量查询使用 CPA 的宿主 HTTP 回调和全局代理。启用代理池或凭据配置了 `proxy_url` 时，查询通过插件传输，独立代理优先；代理连接失败会显示查询错误，不会绕过指定代理直连。查询也计入相应出口组的请求和并发预算。
- 用量查询不参与模型推理、凭据轮转或冷却；它只影响额度暂停状态。

## 测试包

普通分支推送只触发 GitHub Actions 构建。可以在对应提交的 workflow run 中下载 `package-linux-amd64` 等平台产物进行测试；正式市场安装仍取已有 Release。测试阶段不创建版本 tag 或 Release。

建议验收：检查两家厂商实际返回的窗口与金额；让一个窗口用尽确认自动暂停与恢复时间；关闭 `pause_on_window_limit` 后确认轮询不再暂停、429 仍暂停；更换 Key 与「重新检测」的清除行为；切换 CPA 明暗主题和窄屏检查卡片布局。
