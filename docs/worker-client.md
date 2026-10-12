# Windows Worker 使用指南

客户端适用于 Windows 10/11 x64 普通用户。GUI 和 TUI 来自同一个入口，共用配对校验、显式同意及连接生命周期；没有长期密钥、自动同意开关、安装演示模式或自动浏览器启动。不要以管理员运行整个客户端。

## 图形界面

双击 `remoteops-worker-gui-windows-amd64.exe`，通过“浏览…”选择可信渠道收到的 JSON，点击“读取文件”。也可从终端指定 `--pairing .\pairing.json`。完整阅读控制方、地址、授权时间、任务、固定软件版本及管理员范围，勾选“我已核对上述授权，同意本次临时协助”，在遮罩框输入一次性、短期、ephemeral Tailscale Auth Key，再点击“同意并连接”。仅打开或读取配对文件不会连接。

连接后选择“最近任务”中的任务，展开其日志，或点击“取消所选任务”。“查看本次授权”显示冻结的完整授权范围；历史可展开至最近100项，默认最近20项。“立即断开”立即请求撤销本次访问，界面保留结果并允许选择新的配对文件。关闭窗口、Alt+F4 或“退出”都会先安全断开及清理，随后退出；清理超过8秒时继续显示清理窗口直至完成。

常用键盘操作：Alt+F 文件路径、Alt+B 浏览、Alt+O 读取、Alt+A 同意、Alt+K 密钥、Alt+C 连接、Alt+D 断开、Alt+T 取消所选、Alt+L 日志、Alt+S 本次授权、Alt+N 新配对、Alt+X 退出。Tab 在可见可用控件间移动；授权/日志区可用箭头与 PageUp/PageDown 阅读。输入密钥时字母快捷键不会替代文本输入。

窗口右上角选择“简体中文 / English”，Alt+G 聚焦语言选择。连接前切换会清除尚未提交的同意与密钥，重新显示完整授权；连接后切换保留会话、所选任务与日志阅读位置。切换立即生效并保存当前用户的 Worker 偏好；保存失败只影响下次启动记忆，界面显示可恢复提示。也可用 `--lang=en-US` 或 `--config .\client-settings.json` 启动，完整优先级及文件格式见[语言设置](language-settings.md)。

## 终端界面

```powershell
.\remoteops-worker-windows-amd64.exe
.\remoteops-worker-windows-amd64.exe --pairing .\pairing.json
.\remoteops-worker-windows-amd64.exe --ui=tui --pairing .\pairing.json
.\remoteops-worker-windows-amd64.exe --ui=plain --pairing .\pairing.json
.\remoteops-worker-windows-amd64.exe --help
```

TUI 核对授权后按 Y 同意、N 拒绝并回到文件输入。单按 Enter 不会同意。输入密钥显示遮罩，按 Enter 提交。上下键选择任务，L 展开所选日志，C 取消所选任务，D 打开断开确认后按 Y 确认，H 展开历史，? 查看帮助，Esc 返回，Ctrl+C 断开并退出。F2 打开语言选择，1 选择中文、2 选择 English、Esc 返回；密钥输入期间 F2 不执行语言切换，Ctrl+C 仍可立即退出。窄终端可以滚动；`NO_COLOR` 环境变量可关闭配色。

plain 模式在交互终端逐行显示当前语言，中文必须输入“同意”，English 必须输入 `AGREE`，回车或另一语言的确认词不能授权；断开确认也用当前语言的完整确认词。密钥隐藏且不回显。普通路径/授权提示及连接后的命令处可输入 `lang zh-CN` / `lang en-US`；密钥输入时这些字符只作为输入数据，不作为命令。连接后的提示提供 `c 编号` 取消、`l 编号` 日志、`h` 历史、`d` 确认断开及 `?` 帮助，切换语言保留取消编号。终端能力不足时自动使用 plain；重定向输入不会获得授权。GUI EXE 可接受 `--ui=tui|plain`，但其 Windows GUI 子系统不保证提供控制台，终端使用建议选择上述 console EXE。

## 进度、取消与权限

已知总长度的下载显示实际已下载字节/总字节及其比例；未知总长度只显示已下载字节与活动指示，不推测百分比。校验下载、等待 UAC、安装、版本验证分别显示真实阶段及用时，安装无虚构百分比。终态区分成功、需要重启、失败、取消、授权过期及心跳失联；系统检查显示“检查完成”。结果不明确时保留未知状态，不自动重复安装。

取消只作用于当前所选任务。取消或断开不保证回滚已经发生的软件修改；成功且需要重启时不自动重启。管理员操作须同时符合冻结范围、操作员审批和本机逐项 UAC 决策；通信程序保持普通用户权限，内部 `--elevated-helper` 只供受控安装使用，不能与 UI、配对或位置参数混用。客户端没有接收 Auth Key 的参数。

配对 JSON 含一次性敏感凭据，使用后妥善删除。密钥提交后从输入控件清除，显示数据与日志脱敏。授权撤销先于节点清理；Controller 报告 `cleanup_pending` 时由操作员查看并重试清理。

原回环 HTTP 诊断 handler 及其令牌/Origin 安全测试仍保留在 `cmd/worker/local.go`，供将来诊断接入使用；生产新入口不启动该路由或自动浏览器。

真实异地网络、Windows 10/11 UAC 决策、真实安装及回滚限制仍需按[人工验收矩阵](test-plans/manual-acceptance.md)验证。本次界面证据使用模拟任务与惰性测试连接，见[测试报告](test-plans/worker-client-2026-10-11.md)。

双语网页、原生 GUI、ConPTY、plain 和发布产物的惰性验证见[2026-10-12 双语测试报告](test-plans/system-i18n-2026-10-12.md)。程序显示文案可切换；软件名、设备名、用户输入、任务 ID、原始安装器日志和 Windows UAC 系统窗口保留原值。
