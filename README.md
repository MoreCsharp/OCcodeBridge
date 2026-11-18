<div align="center">

<img src="logo.png" width="96" height="96" alt="OCcodeBridge">

# OCcodeBridge

把 OpenCode Go 与 CommandCode 订阅套餐接入 CPA，并在 CPA 里直接看到 5 小时 / 每周 / 每月额度。

[![Release](https://img.shields.io/github/v/release/xiao-qiu-qiu/OCcodeBridge?color=f0a03c)](https://github.com/xiao-qiu-qiu/OCcodeBridge/releases)
[![Platforms](https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-64748b)](https://github.com/xiao-qiu-qiu/OCcodeBridge/releases)
[![License](https://img.shields.io/github/license/xiao-qiu-qiu/OCcodeBridge?color=16a34a)](LICENSE)

[下载](https://github.com/xiao-qiu-qiu/OCcodeBridge/releases) · [反馈问题](https://github.com/xiao-qiu-qiu/OCcodeBridge/issues)

</div>

OCcodeBridge 是 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI) 的动态库插件，自带网页管理界面。

两家套餐本身都提供 API 访问，但直接在 CPA 里把它们当普通 OpenAI 兼容上游接入，就看不到套餐的额度窗口，窗口用完后还可能悄悄扣付费额度。插件补上的就是这一块：

- 读取每个账号的套餐额度。
- 在 CPA「认证文件」页和插件管理页显示额度。
- 窗口用尽时自动暂停该账号。

## 功能

- **两家厂商，一个插件**：每个凭据自选厂商（OpenCode Go / CommandCode），可混合使用多个账号，按 CPA 的优先级和会话黏性调度。
- **额度可见**：
  - 5 小时、每周、每月三个窗口，显示已用比例、已用 / 上限和重置时间。
  - CPA「认证文件」页同步显示各窗口的剩余比例。
- **窗口额度保护**（默认开启）：任一窗口用尽即暂停该账号，到重置时间自动恢复，避免溢出到 OpenCode Extra Usage 或 CommandCode 额外额度。
- **套餐无 API 权限自动识别**：CommandCode Go 套餐没有 API 权限，插件会长期停用该凭据，不当作临时冷却反复重试。升级套餐后点「重新检测」即可恢复。
- **模型映射**：
  - 客户端模型名 → 带厂商前缀的上游模型 ID，可从上游一键拉取模型列表。
  - 支持思考强度映射。
- **请求日志与统计**：记录以下信息。
  - 厂商、上游模型、首字耗时、输出速度、缓存命中率。
  - OpenCode Go 的本次计入额度（`opencode.allowance`）。
- **其他**：
  - 统一错误规则。
  - 可选的流式卡住提前截流。
  - CommandCode 零数据保留（ZDR）开关。
  - 实验性出口代理池。

## 安装

需要支持动态库插件（JSON ABI v1）的原版 CPA，已验证 v8.0.67。

1. 从 [Releases](https://github.com/xiao-qiu-qiu/OCcodeBridge/releases) 下载对应平台的压缩包，用 `checksums.txt` 校验，然后把里面的文件放进 CPA 的插件目录：

   | 平台 | 文件 |
   | --- | --- |
   | Linux amd64 / arm64 | `occodebridge.so` |
   | macOS amd64 / arm64 | `occodebridge.dylib` |
   | Windows amd64 | `occodebridge.dll` |

2. 在 CPA 配置中启用插件并重启 CPA：

   ```yaml
   plugins:
     enabled: true
     dir: plugins
   ```

3. 打开插件管理页 `http://<CPA 地址>/v0/resource/plugins/occodebridge/console`。管理页会尝试复用 CPA 管理中心保存的登录信息，需要输入时填 CPA 管理密钥。

## 快速上手

1. **添加凭据**：在「凭据管理」中选择厂商，填入 API Key。

   | 厂商 | API Key 获取位置 | 说明 |
   | --- | --- | --- |
   | OpenCode Go | [opencode.ai](https://opencode.ai) 控制台的 Go 套餐页 | 每周额度在周一 00:00 UTC 重置 |
   | CommandCode | [commandcode.ai](https://commandcode.ai) 账号后台（与 CLI 同一把 Key） | 需要 GOAT 及以上套餐，Go 套餐没有 API 权限 |

2. **添加模型**：在「模型映射」中点「添加模型 → 获取上游模型」，选择上游模型并起一个客户端模型名，例如：

   | 客户端模型名称 | 上游模型 ID |
   | --- | --- |
   | `deepseek-flash` | `opencode-go/deepseek-v4.1-flash` |

   上游 ID 的第一段是厂商前缀，发往上游时会自动去掉。首次安装不预置任何模型。

3. **调用**：客户端填 CPA 的 API 地址和 CPA 的客户端 API Key，向 `/v1/chat/completions` 发送请求，`model` 填客户端模型名称：

   ```json
   {
     "model": "deepseek-flash",
     "messages": [{"role": "user", "content": "你好"}],
     "stream": true
   }
   ```

   流式与非流式都支持。

## 额度说明

| 厂商 | 窗口 | 单位 |
| --- | --- | --- |
| OpenCode Go | 5 小时 / 每周 / 每月 | 美元 |
| CommandCode | 5 小时 / 每周（滚动），每月 | Go / GOAT / Pro 为套餐积分，其他套餐 1 积分 = $1 |

- 额度每分钟左右在后台采样一次，管理页可手动刷新。
- CommandCode 的套餐积分按模型折算：每个模型有自己的月度额度，同样 1 积分在不同模型上能用的金额不同，所以管理页显示为「已用 / 上限 积分」，不标美元。
- CommandCode 的购买 / 赠送额度单独显示为「额外额度」。这部分不受窗口限制，窗口用尽后上游会自动扣用，保护开关就是为此准备的。
- 采样有间隔，窗口刚用尽到插件暂停之间，可能还会产生少量超额消费。
- 两家的用量接口都不是公开文档化的 API，接口变化时管理页会显示查询失败，不会伪造数值。

## 常用设置

在管理页「配置概览」中修改，也可通过 `PUT /v0/management/occodebridge/config` 只提交要改的字段。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `pause_on_window_limit` | `true` | 窗口用尽时暂停账号。关闭后只在上游返回超限 429 时暂停 |
| `nonstream_mode` | `stream-aggregate` | 非流式请求的处理方式：`stream-aggregate`（聚合上游流式结果）、`native`、`native-fallback` |
| `stream_idle_cutoff_enabled` | `false` | 流式输出卡住时提前结束请求 |
| `stream_idle_timeout_seconds` | `20` | 提前截流的空闲阈值，单位秒，范围 1–1800 |
| `base_url_overrides` | 空 | 按厂商替换推理地址，仅用于自建代理或测试 |
| `debug` | `false` | 额外记录流式诊断信息 |

## 数据与备份

| 位置 | 内容 |
| --- | --- |
| CPA 的 `auth-dir` | 凭据：API Key、厂商、优先级 |
| `plugins/occodebridge-data` | 模型映射、设置、日志与统计 |
| `plugins` | 插件动态库 |

升级前请备份以上目录。容器部署时需要持久化这三处。

## 从源码构建

需要 Go 1.26 和 C 工具链（插件以 `-buildmode=c-shared` 构建）。

```bash
# Linux（需要 gcc）
scripts/build-linux.sh

# 或使用容器构建
docker build --platform linux/amd64 -f Dockerfile.build --output type=local,dest=dist .
```

```powershell
# Windows（MinGW-w64 gcc）
pwsh -NoProfile -File scripts/build-windows.ps1 -Zip
```

产物在 `dist/`。其他平台的打包方式见 [发布工作流](.github/workflows/release.yml)。

## 许可

[MIT License](LICENSE)。厂商接口参考各厂商公开文档与社区公开的协议观察，未复制第三方代码。

## 友情链接

[LINUX DO](https://linux.do)
