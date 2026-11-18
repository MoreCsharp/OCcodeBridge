# internal/vendors 契约

`internal/vendors` 是 PlanBridge 与订阅厂商之间的唯一边界。**本包及其子包不得 import `internal/bridge`**，这样每个厂商包可以由不同的人并行开发、测试，而互不影响。

## 谁负责什么

- **bridge**：读取配置与凭据、选择凭据、套用出口层（宿主回调 / 代理池）、组装请求体、SSE 解析、日志、额度暂停、CPA 的 quota.* 映射、页面渲染。
- **vendor 包**：厂商 id 与显示名、推理 base URL、厂商专有请求头、模型目录、用量读取、窗口超限 429 的判定、响应里的厂商专有字段（如 allowance）。

vendor 包**不得自己创建 `http.Client` 访问网络**，也不得读写文件或日志。所有 HTTP 都要经过调用方注入的 `HTTPDoer`，测试用 `httptest` 或 fake doer 替换。

## 接口

```go
type Vendor interface {
	ID() string
	DisplayName() string
	BaseURL() string
	Headers(r Request) http.Header
	ListModels(ctx context.Context, c Credential, doer HTTPDoer) ([]Model, error)
	FetchUsage(ctx context.Context, c Credential, doer HTTPDoer) (Usage, error)
	ClassifyLimitError(status int, body []byte, now time.Time) *LimitBlock
	ObserveResponse(frame map[string]any) *Observation
}
```

- `ID()`：凭据上保存的厂商 id（`opencode-go` / `commandcode`），也是模型 UpstreamID 的前缀。目录必须叫 `vendors` 而不是 `vendor`：Go 会拒绝导入名为 `vendor` 的子目录。
- `BaseURL()`：推理 base，例如 `https://opencode.ai/inference/go/v1`；路径由 bridge 追加（`/chat/completions`、`/models`）。`Config.BaseURL` 非空时作为覆盖优先（只用于测试与自建代理）。
- `Headers(Request)`：只返回厂商拥有的头。`User-Agent`、`Content-Type` 由 bridge 补齐（UA 固定为 `PlanBridge/<Version>`），vendor 返回同名头则以 vendor 为准。
- `ListModels`：返回目录，`Model.UpstreamID` 必须带前缀（用 `vendors.UpstreamID(ID(), id)`）。只返回可用的模型；不支持的端点模型要么不返回，要么把 `Endpoints` 如实填好，由 bridge 过滤。
- `FetchUsage`：返回 `Usage`。**缺字段必须留 nil，不得补 0**，否则界面会把"未知"显示成"已用尽"。`AccountID` 用于把同一订阅的多个 key 归并成一个样本。
- `ClassifyLimitError`：只在**窗口额度耗尽**时返回 `*LimitBlock`；普通瞬时限流、鉴权失败、套餐不支持 403 等一律返回 nil（其中 `Permanent: true` 表示"用量轮询也无法恢复"，bridge 会停止调度而不是冷却）。
- `ObserveResponse`：从一帧（非流式顶层 JSON 或流式单个 chunk）里读取厂商专有事实，没有就返回 nil。

## 共享类型约定

- `Window.Kind` 只能是 `5h` / `7d` / `daily` / `month`。bridge 映射到 CPA quota 时：`5h`→`5h`、`7d`→`7d`、`month`→月度；`daily` 没有 CPA 窗口，只用于内部暂停判断与页面展示。
- 金额字段 `Used`/`Cap`/`Balance` 只在厂商确实给出金额时填写，币种由 `BalanceCurrency` 说明。
- `Model.Endpoints` 使用厂商文档里的原样路径（如 `/chat/completions`、`/messages`）；空表示未知。
- 厂商 id 与模型 id 之间的前缀切分统一用 `vendors.SplitUpstreamID`：只切第一段，因此 `commandcode/deepseek/deepseek-v4-flash` 的模型 id 是 `deepseek/deepseek-v4-flash`。

## 注册

每个厂商包在自己的 `init()` 里调用 `vendors.Register(...)`；bridge 用空 import 拉入两个厂商包，再用 `vendors.Get(id)` / `vendors.IDs()` 解析凭据上的 `vendor` 字段。`Register` 对重复 id 直接 panic，因为这只能是编码错误。

## 新增一个厂商

1. 新建 `internal/vendors/<id>/`，实现 `Vendor`。
2. 写最小测试：id/显示名/base URL/头，以及各方法的离线用例（`httptest` + 自己的 `HTTPDoer` 实现）。
3. 在 `internal/bridge/vendor_wiring.go` 的空 import 里加上这个包。
4. 不需要改 bridge 其他文件：凭据的厂商下拉来自 `vendors.IDs()`。
