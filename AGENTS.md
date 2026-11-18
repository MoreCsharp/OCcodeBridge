# PlanBridge 开发边界

- 每完成一个功能并通过必要验证后，立即提交相关改动，提交信息默认使用中文。
- 本项目只维护插件，默认面向支持动态库插件的原版 CPA。
- 使用 CPA 现有公开插件接口；不为插件编写、维护、打包或发布 CPA 宿主补丁。
- 遇到插件接口覆盖不到的需求，先向用户说明具体限制，缩小或绕开该功能；不通过修改宿主来补齐。
- 账号与模型调度、冷却等宿主行为，以实际接口和宿主结果为准；文档与界面不承诺插件未实现的能力。
- 保留已有的可选字段兼容和缺字段回退，避免为清理历史补丁引入无关运行行为变更。

## 厂商适配层

- 统一接口在 `internal/vendors`（包名 `vendors`），每个厂商一个子包：`internal/vendors/<id>`。
  - 目录必须叫 `vendors` 而不是 `vendor`：Go 会把名为 `vendor` 的子目录当作依赖目录并拒绝导入。
- 厂商包只依赖标准库和 `internal/vendors`：
  - 不导入 `internal/bridge`，也不读取插件配置、日志或凭据存储。
  - 不自己创建 `http.Client`：所有网络请求都通过注入的 `vendors.HTTPDoer`，由桥接层负责超时、代理池和日志。
- 厂商包在 `init()` 里调用 `vendors.Register` 自注册；重复 id 会 panic，新增厂商须同步更新 `internal/bridge/vendor_wiring.go` 的空白导入。
- 桥接层只通过接口调用厂商，不写 `if vendor == ...` 分支；厂商差异必须落在厂商包内。
- 未实现的能力返回 `vendors.ErrNotImplemented`，由桥接层翻译成 501，不要返回空结果冒充成功。

## 凭据与模型标识

- 凭据的 `vendor` 是调度必需字段：缺失或未知时拒绝调度，并在界面提示选择厂商。
- 模型的上游 ID 必须带厂商前缀（`<vendor>/<model>`），校验失败按 400 处理；发往上游前由 `executor.go` 去掉第一段。
- 上游返回的额度窗口种类只有 `5h` / `7d` / `daily` / `month`，转成 CPA 配额窗口时映射为 `5h` / `7d` / `daily` / `monthly`。
- 旧配置文件里已删除的字段（`providers`、`has`、`routing_mode`、`provider_options` 等）按未知字段忽略，不得报错。
