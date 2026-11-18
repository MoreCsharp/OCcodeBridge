# internal/vendors/opencodego

OpenCode Go 的 `vendors.Vendor` 实现。接口契约见 `../README.md`；本文件只记录本包的取舍与**待真实 key 核实**项。

## 方法分工

| 文件 | 内容 |
|---|---|
| `opencodego.go` | id / 显示名 / 推理 base / 请求头 |
| `session.go` | `x-opencode-session` 的稳定化：客户端会话头优先，否则 `sha256(凭据 ID + 首条 system + 首条 user)` 截断 32 位十六进制；消息内容各限 4 KiB，支持字符串与 parts 数组 |
| `models.go` | `GET {BaseURL}/models` → `UpstreamID` 带 `opencode-go/` 前缀，`Endpoints` 固定 `["/chat/completions"]`（文档称所有文本模型都可由 Console 转换） |
| `usage.go` | `GET https://opencode.ai/zen/go/v1/usage`（变量 `usageEndpoint`，测试可指向本地） |
| `limit.go` | 429 窗口超限判定 |
| `observe.go` | `opencode.allowance` 观测 |

## 容错策略

- 用量：先读 API-key 形状 `{usage:{rolling,weekly,monthly}}`，没有再读旧控制台形状 `{access:{meters:{fiveHour,week,month},endsAt}}`。缺窗口不生成，缺字段留 `nil`（绝不补 0）；`percent` 夹到 0–100；`used`/`cap` 与 `usedMicroCents`/`limitMicroCents`（÷1e8 转美元）都接受，仅在无 `percent` 时用二者推算。`resetsAt` 接受 ISO 字符串、unix 秒、unix 毫秒。周窗口缺 `resetsAt` 时按「下一个周一 00:00 UTC」推算并把 `Status` 标为 `derived`。全部窗口缺失返回 `usage response unrecognised`。
- 账号归并：有 plan/workspace 字段就用；否则 `AccountID` 取 key 的 sha256 前缀（`key-<16 hex>`），不含明文 key。
- 模型目录：非 2xx 返回带 HTTP 状态码的错误（实现 `StatusCode()`，bridge 直接映射 401/403），错误信息里的响应片段会把 key 打码；未知字段忽略，无 `id` 的条目丢弃。
- 429：仅 `status == 429` 且 error type/name/code 含 `GoUsageLimit` 或 message 含 usage limit / 5-hour / rolling / weekly / monthly 等才判为窗口超限；普通 `rate_limit_error`、401/403、非 JSON 一律返回 `nil`。重置时间依次尝试 `resetsAt`/`resetAt`/`reset`（含 `error.rateLimit` 嵌套）、`retryAfter`（秒数或时间戳）、message 内的 ISO 时间或 `in Xh Ym`。
- `opencode.allowance`：接受 `frame.opencode.allowance`、`frame.usage.opencode(.allowance)`、`frame.usage.allowance` 与裸 `opencode` 数值；数值统一成 `{"value": n}`，`Raw` 保留片段 JSON，没有则不产生观测。

## 待真实 key 核实（规格 §5）

1. `GET /zen/go/v1/usage` 的真实响应结构（`usage.{rolling,weekly,monthly}` 是否存在、`percent` 单位、`resetsAt` 格式）。
2. 429 窗口超限的真实响应体（`GoUsageLimitError` 的 type/code 名与重置字段名）。当前判定与解析均为容错推测。
3. `opencode.allowance` 的真实位置与单位（当前按数值原样记录，不做金额换算）。
4. 周窗口「下一个周一 00:00 UTC」来源于文档明文，但 `derived` 状态只是本插件对推算值的标注，非上游字段。

以上任一项在真实 key 下确认后，请同步收紧 `usage.go` / `limit.go` 的解析并保留现有宽容分支（缺字段回退不得删除）。

## 测试

`go test -timeout 120s ./internal/vendors/opencodego` 全部离线：`testdata/*.json` 为上游响应 fixture，HTTP 一律走注入的 `HTTPDoer`（`httptest` 本地服务器），不访问 opencode.ai。
