<div align="center">

# PlanBridge

把订阅型编码套餐接到 CPA，在你常用的 OpenAI 兼容客户端里调用。

[![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-64748b)](https://github.com/xiao-qiu-qiu/PlanBridge/releases)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/github/license/xiao-qiu-qiu/PlanBridge?color=16a34a)](LICENSE)

[使用文档](docs/configuration.md) · [更新日志](CHANGELOG.md) · [反馈问题](https://github.com/xiao-qiu-qiu/PlanBridge/issues)

</div>

本项目是 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 的插件，带一个网页管理界面。插件不绑定某一家上游：每个凭据自己选**厂商**，请求就发往该厂商的订阅套餐。当前内置 **OpenCode Go** 与 **CommandCode** 两家。

> **用量接口为非公开接口。** OpenCode Go 的 `zen/go/v1/usage` 与 CommandCode 的 `/alpha/*` 用量接口都是社区观察到的内部接口，本版本只用合成样本和本地假上游验证过，**尚未用真实付费 key 全量核实**。解析保持宽容：字段缺失按「未知」显示，不会补 0 或推算金额。接口变更时以管理页的查询状态为准。

## 本项目能做什么

- **两家厂商统一接入**：`internal/vendors` 定义统一接口（基础地址、请求头、模型目录、额度、限流与响应观察），每家厂商一个子包，互不影响。
- **管理多个凭据**：每个凭据必须选择厂商，可设优先级、API Key、代理和 ZDR；额度用完自动暂停，额度恢复自动恢复，套餐没有 API 权限时长期停用。
- **映射模型**：客户端模型名 → 上游模型 ID。上游 ID 必须带厂商前缀（例如 `opencode-go/deepseek-v4.1-flash`），发往上游时自动去掉前缀。还能配置思考强度映射。
- **查看请求情况**：记录日志，显示厂商、上游模型、首字耗时、输出速度和缓存命中率；OpenCode Go 响应里的 `opencode.allowance` 会作为「本次计入额度」记录在日志里。
- **调整请求行为**：统一错误规则、提前截断流式卡住的请求。另提供实验性代理池，按需开启。

## 安装与配置

需要支持动态库插件和 JSON ABI v1 的原版 CPA。先在 CPA 配置里启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins
```

1. 把插件动态库放进 CPA 的 `plugins` 目录（`planbridge.dll` / `planbridge.so` / `planbridge.dylib`），或从 [Releases](https://github.com/xiao-qiu-qiu/PlanBridge/releases) 下载对应平台的 ZIP 手动放置。发布附带 `checksums.txt`。
2. 打开管理页 `/v0/resource/plugins/planbridge/console`，在「凭据管理」中添加 API Key 并选择厂商。
3. 进入「模型映射」，点「添加模型 → 获取上游模型」按厂商拉取候选，或直接手填带前缀的上游 ID。
4. 在客户端填入 **CPA 的 API 地址和 CPA 的客户端 API key**，选择刚添加的模型即可。

首次安装**不预置任何模型映射**，也未选择厂商的旧凭据不参与调度。

管理页会尝试复用 CPA / CPAMP 的登录信息。如果提示输入密钥，直连 CPA 时填 CPA 管理密钥，经 CPAMP 完整模式访问时填面板管理密钥。

### 两家厂商怎么拿 Key

| 厂商 | 在哪里拿 API Key | 套餐限制 | 插件如何处理 |
| --- | --- | --- | --- |
| OpenCode Go | [opencode.ai](https://opencode.ai) 控制台的 Go 套餐页签发 API Key | 有 5 小时 / 每周 / 每月额度；每周额度在周一 00:00 UTC 重置；超额可能转用付费 extra usage | 每个请求带稳定的 `x-opencode-session` 会话头，便于上游缓存与计费；额度窗口满时按 `pause_on_window_limit` 暂停 |
| CommandCode | [commandcode.ai](https://commandcode.ai) 账号后台的 API Key（CLI 与 API 共用一把） | **Go 套餐没有 API 权限**，需要 GOAT 及以上；额度以 credits（约等于美元）计 | 上游明确表示「套餐无 API 权限」时长期停用该凭据，重启不丢失，升级套餐后点「重新检测」或换 Key 解除 |

CommandCode 的凭据可以打开 **ZDR**（零数据保留），插件会把 `x-cmd-zdr` 发给上游。OpenCode Go 没有这个开关。

## 调用模型

假设你添加了这条模型映射：

| 客户端模型名称 | 上游模型 ID |
| --- | --- |
| `deepseek-flash` | `opencode-go/deepseek-v4.1-flash` |

向 CPA 的 `/v1/chat/completions` 发送请求，`model` 填客户端模型名称：

```json
{
  "model": "deepseek-flash",
  "messages": [{"role": "user", "content": "你好"}],
  "stream": true
}
```

流式与非流式都支持。非流式默认由插件把上游流式结果聚合成一个响应（`nonstream_mode: stream-aggregate`），也可以改成 `native` / `native-fallback` 直接请求上游非流式接口。

## 用之前看一眼

- **用量接口没有真实 key 全量核实。** 见上文；额度卡片显示的是厂商报告的百分比或已用/上限，不按价格推算。
- **窗口额度保护默认开启。** 任一 5 小时 / 每周 / 每天 / 每月窗口用尽时暂停该账号调度，避免把额度花到付费 extra usage 上；`pause_on_window_limit: false` 可关闭主动暂停。
- **流式卡住时提前截流默认关闭。** 开启后，从首段有效输出后计时，默认连续 20 秒没有新的有效输出才触发。
- **Debug 默认关闭。** 按需开启额外流诊断；普通请求日志、错误和用量照常记录。
- **升级前保留数据。** 容器部署请持久化插件目录、CPA 的 `auth-dir` 和插件数据目录（默认 `plugins/planbridge-data`），升级前备份。

| 文档 | 内容 |
| --- | --- |
| [进阶配置](docs/configuration.md) | 登录、备份、模型映射、思考强度、错误规则和日志 |
| [套餐用量](docs/plan-usage.md) | 两家厂商的用量接口、窗口种类和展示方式 |
| [调度与代理出口](docs/scheduling.md) | 凭据调度、额度保护、代理池和限流 |
| [CPA 兼容性](docs/cpa-compatibility.md) | 支持的接口、流式错误、重试和冷却行为 |
| [更新日志](CHANGELOG.md) | 各版本要点 |
| [厂商适配层](internal/vendors/README.md) | 接口契约与新增厂商的步骤 |

## 从源码构建

项目使用 Go 1.26，插件是动态库（`-buildmode=c-shared`），需要 C 工具链。

Linux amd64，装好 Docker 后在仓库根目录执行：

```bash
docker build --platform linux/amd64 -f Dockerfile.build --output type=local,dest=dist .
```

产物是 `dist/planbridge.so`。

Windows amd64，用本地 winlibs MinGW-w64 gcc：

```powershell
pwsh -NoProfile -File scripts/build-windows.ps1        # dist\planbridge.dll
pwsh -NoProfile -File scripts/build-windows.ps1 -Zip   # 另打 planbridge_<版本>_windows_amd64.zip
```

提交前的一键检查（格式、vet、build、Go 测试、前端测试）：

```powershell
pwsh -NoProfile -File scripts/check.ps1
```

其他平台（Linux arm64、macOS amd64/arm64）的打包方式见[发布工作流](.github/workflows/release.yml)。仓库里还有 `scripts/e2e-native.ps1`：用本地假上游和真 CPA 进程跑一遍原生验收（需要 Python 3 与本地 CPA 可执行文件），不会访问真实上游。

## 许可与致谢

[MIT License](LICENSE)。厂商接口的设计参考了社区公开的协议观察（例如 [cline-pass-switcher](https://github.com/munmunjaklin458-afk/cline-pass-switcher) 等项目的公开行为）与各厂商公开文档，未复制其代码。

## 友情链接

[LINUX DO](linux.do)
