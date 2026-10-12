# 语言设置

RemoteOps 支持 `zh-CN`（简体中文）和 `en-US`（English），默认中文。网页、Worker、Controller、MCP 各自选择语言；网页不会要求远端 Worker 跟随，也不会把语言写入配对、授权或任务签名。

## 从界面选择

| 入口 | 操作 | 保存位置 |
|---|---|---|
| 网页 | 登录页、工作台右上角或“配置”页选择语言 | 当前浏览器 `localStorage` 的 `remoteops.language` |
| Worker Windows GUI | 语言下拉框；Alt+G 聚焦 | Worker 设置文件 |
| Worker TUI | F2，1 中文 / 2 English；Esc 返回 | Worker 设置文件 |
| Worker plain | 普通提示/连接后的命令处输入 `lang zh-CN` 或 `lang en-US` | Worker 设置文件 |

界面选择立即生效并保存。连接前切换会清除尚未提交的同意、Auth Key 或网页管理令牌，要求按新语言重新核对；连接后切换保留会话、任务选择、日志阅读位置和 plain 取消编号。密钥输入期间不解释语言命令或 F2，Ctrl+C 仍可立即断开退出。TUI 两种语言都用 Y/N；plain 中文用“同意”，英文用 `AGREE`，授权和断开确认都不接受空输入或另一语言的确认词。

## 启动参数和设置文件

所有原生入口使用相同优先级：`--lang` → `REMOTEOPS_LANG` → `--config` 指定文件 → 当前用户保存的设置 → `zh-CN`。

```powershell
.\remoteops-worker-windows-amd64.exe --lang=en-US --pairing .\pairing.json
.\remoteops-worker-windows-amd64.exe --ui=plain --config .\client-settings.json
.\remoteops-worker-gui-windows-amd64.exe --lang=zh-CN
.\remoteops-controller-windows-amd64.exe --lang=en-US --help
.\remoteops-mcp-windows-amd64.exe --config .\mcp-settings.json --help
```

设置文件仅包含一个语言字段，不能放入 Auth Key、管理令牌或会话凭据：

```json
{
  "language": "en-US"
}
```

未指定 `--config` 时，路径来自 Go `os.UserConfigDir()` 下的 `RemoteOps` 目录。Windows 通常为以下三个独立文件：

```text
%APPDATA%\RemoteOps\worker-settings.json
%APPDATA%\RemoteOps\controller-settings.json
%APPDATA%\RemoteOps\mcp-settings.json
```

Linux 通常为 `$XDG_CONFIG_HOME/RemoteOps/`（未设置时为 `~/.config/RemoteOps/`），macOS 通常为 `~/Library/Application Support/RemoteOps/`。Worker GUI/TUI/plain 共用 Worker 偏好；Controller 和 MCP 各自读取自己的文件。`--config` 是语言设置文件，不能替代 `--pairing`、软件目录或 MCP 的 `--token-file`。

例如文件保存中文、`REMOTEOPS_LANG=en-US` 时本次启动为英文，再加 `--lang=zh-CN` 则本次为中文。参数和环境变量只覆盖当前进程，不改写文件。要测试示例环境变量，可在独立终端设置 `$env:REMOTEOPS_LANG='en-US'`，测试结束用 `Remove-Item Env:REMOTEOPS_LANG` 清除。

显式输入都会验证，即使被高优先级覆盖：`--lang=en-US --config .\损坏的设置.json` 仍在联网前报错，不忽略文件；非法 `REMOTEOPS_LANG` 也不会被 `--lang` 掩盖。语言只接受上述两个准确值；空值、重复字段、未知字段、非字符串、额外 JSON 和过大的设置文件均无效。

## 保存失败与重启

指定 `--config` 时，Worker 界面选择保存回该文件；否则保存到默认 Worker 文件。保存使用同目录临时文件和原子替换，成功前保留旧设置。CLI/env 覆盖不静默保存；从界面选择才会尝试保存。

显式设置不存在、不可读或无效会在启动前报错；隐式保存文件不存在表示首次运行，损坏或不可读则回退中文并显示可恢复提示。原生文件或浏览器存储保存失败时，本次切换仍生效，提示只影响记忆功能；修复目录权限/浏览器存储后重新选择即可重试。重启时按同一优先级重新读取，故未成功保存的选择不保证保留。浏览器存储读取失败也默认中文，仍允许切换。

语言不会改变 API 枚举、JSON 字段、MCP 工具名/参数、审批或执行行为。软件名、固定版本、设备名、用户输入、URL、任务 ID、安装器/操作系统原始日志保持原值；Windows UAC 系统窗口使用 Windows 自身的语言。程序提供本地化说明，不翻译外部日志。MCP stdout 保持 JSON-RPC 协议输出，帮助/诊断使用对应输出通道；内部 `--elevated-helper` 在语言初始化前分流，拒绝与语言、配置、UI 或配对参数混用。

测试范围和当前限制见[双语惰性验收报告](test-plans/system-i18n-2026-10-12.md)。
