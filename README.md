<div align="center">

# PlanBridge

把订阅型编码套餐接到 CPA，在你常用的 OpenAI 兼容客户端里调用。

[![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-64748b)](https://github.com/xiao-qiu-qiu/PlanBridge/releases)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/github/license/xiao-qiu-qiu/PlanBridge?color=16a34a)](LICENSE)

[使用文档](docs/configuration.md) · [反馈问题](https://github.com/xiao-qiu-qiu/PlanBridge/issues)

</div>

> **开发中。** 当前是 P0 骨架：请求链路、凭据、模型映射、错误规则、代理池和日志已可用；厂商适配层已独立成包，两个内置厂商仍是占位实现，尚不能真正拉取额度或目录。

本项目是 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 的插件，带一个网页管理界面。插件本身不绑定某一家上游：请求由凭据上的**厂商**决定发往哪里。

## 本项目能做什么

- **厂商适配层**：`internal/vendors` 定义统一接口（基础地址、请求头、模型目录、额度、限流与响应观察），每个厂商一个子包，互不影响。内置 `opencode-go` 与 `commandcode`，P1/P2 直接在各自子包里补实现。
- **管理多个凭据**：每个凭据必须选择厂商，可设优先级、API Key 和代理；额度用完自动暂停，额度恢复自动恢复。
- **映射模型**：客户端模型名 → 上游模型 ID，上游 ID 必须带厂商前缀（例如 `opencode-go/deepseek-v4.1-flash`），发往上游时自动去掉前缀。还能配置思考强度映射。
- **查看请求情况**：记录日志，显示厂商、上游模型、首字耗时、输出速度和缓存命中率。
- **调整请求行为**：统一错误规则、提前截断流式卡住的请求。另提供实验性代理池，按需开启。

## 安装与配置

需要支持动态库插件和 JSON ABI v1 的原版 CPA。先在 CPA 配置里启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins
```

1. 把插件放进 CPA 的 `plugins` 目录，或用仓库里的发布工作流构建后手动放置。
2. 打开管理页 `/v0/resource/plugins/planbridge/console`，在「凭据管理」中添加 API Key 并选择厂商。
3. 进入「模型映射」，点「添加模型 → 获取上游模型」按厂商拉取候选（厂商未实现时会提示手动填写），或直接手填上游 ID。
4. 在客户端填入 **CPA 的 API 地址和 CPA 的客户端 API key**，选择刚添加的模型即可。

首次安装**不预置任何模型映射**，也未设置厂商的旧凭据不参与调度。

管理页会尝试复用 CPA / CPAMP 的登录信息。如果提示输入密钥，直连 CPA 时填 CPA 管理密钥，经 CPAMP 完整模式访问时填面板管理密钥。

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

## 用之前看一眼

- **厂商未实现的部分会明确报错。** 占位厂商的额度查询和模型目录返回「尚未实现」，不会静默回退到别的上游。
- **流式卡住时提前截流默认关闭。** 开启后，从首段有效输出后计时，默认连续 20 秒没有新的有效输出才触发。
- **Debug 默认关闭。** 按需开启额外流诊断；普通请求日志、错误和用量照常记录。
- **升级前保留数据。** 容器部署请持久化插件目录、CPA 的 `auth-dir` 和插件数据目录（默认 `plugins/planbridge-data`），升级前备份。

| 文档 | 内容 |
| --- | --- |
| [进阶配置](docs/configuration.md) | 登录、备份、模型映射、思考强度、错误规则和日志 |
| [套餐用量](docs/plan-usage.md) | 厂商返回的额度窗口与展示方式 |
| [调度与代理出口](docs/scheduling.md) | 凭据调度、额度保护、代理池和限流 |
| [CPA 兼容性](docs/cpa-compatibility.md) | 支持的接口、流式错误、重试和冷却行为 |
| [厂商适配层](internal/vendors/README.md) | 接口契约与新增厂商的步骤 |

## 从源码构建

项目使用 Go 1.26。装好 Docker 后，在仓库根目录执行：

```bash
docker build --platform linux/amd64 -f Dockerfile.build --output type=local,dest=dist .
```

产物是 `dist/planbridge.so`（Linux amd64）。其他平台的构建方式见 [发布工作流](.github/workflows/release.yml)。

## 许可与致谢

[MIT License](LICENSE)。厂商接口的设计参考了 [cline-pass-switcher](https://github.com/munmunjaklin458-afk/cline-pass-switcher) 等项目的公开协议行为，未复制其代码。

## 友情链接

[LINUX DO](linux.do)
