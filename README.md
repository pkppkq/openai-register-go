# openai-register-go

这是 `openai-register-paylink-ui` 从 **Python/Tkinter** 到 **Go 1.26 + Wails v2.12.0 + Svelte** 的完整桌面端重构。它已经不再是早期 POC：业务后端、状态存储、并发任务、浏览器控制和操作页面均已迁移到 Go/Wails，发布版可构建为一个便携式 Windows EXE。

构建后的 EXE 不需要安装 Python、Go、Node.js，也不需要安装程序；前端资源已经嵌入 EXE。界面运行依赖 Microsoft Edge WebView2，浏览器自动化依赖 Google Chrome 或 Chromium。EXE 本身可以移动或复制，但状态和敏感数据独立保存在用户数据目录中。

> 仅在你有权操作的账号、邮箱、代理和支付资料上使用本工具，并遵守相关服务条款。注册、租号、代理预检、Team 席位、checkout 和支付操作可能产生真实费用。

本项目是社区维护的非官方工具，与 OpenAI 没有隶属、授权、赞助或背书关系；项目名称只用于说明兼容的服务场景。

## 这次重构了什么

| 范围 | Python/Tkinter 旧版 | Go/Wails 重构版 |
|---|---|---|
| 桌面界面 | Tkinter 单体窗口 | Wails v2 + Svelte，前端通过生成的 TypeScript 绑定调用 Go |
| 业务代码 | 大量逻辑集中在 `app.py` | 按账号、认证、邮箱、接码、代理、支付、导出和状态等领域拆分到 `internal/*` |
| 并发与取消 | Python 线程、队列和 Tk 定时轮询 | goroutine、上下文取消、任务注册表和 Wails 事件日志 |
| HTTP 指纹 | Python HTTP/TLS 方案 | `bogdanfinn/tls-client`，统一 Chrome TLS/HTTP2 指纹与代理路由 |
| 浏览器自动化 | Python 浏览器驱动 | `go-rod/rod`，支持独立/持久化浏览器、指纹注入、代理和解压扩展 |
| 本地数据 | `state.json` 及拆分 Session | 保持旧格式兼容，原子写入、延迟保存、旧版自动迁移和 Session 分文件存储 |
| 发布方式 | Python 环境和依赖 | 前端资源嵌入单个 EXE；运行数据仍独立保存在用户目录 |
| 风险控制 | 部分高风险动作可直接执行 | 删除、付费、消耗额度和不可逆操作增加显式确认 |

当前代码覆盖的主要能力包括：

- 账号导入、分组、筛选、排序、详情、账号类型管理、别名和域名邮箱生成；
- 注册/登录、协议注册取 Session、OAuth 授权取 RT、登录并保留浏览器、辅助读取 Session、外部 OAuth 和手动登录取码；
- Session 手工合并/替换、刷新、套餐识别及支付链接生成；
- IMAP/Cloud Mail 邮箱读取、验证码和邀请链接提取、停用通知扫描；
- 手机号池、手工取码和 SMSBower 配置/只读检测；
- 本地链式代理、分阶段代理池、提供商代理池预热、代理预检与清理；
- 支付资料和卡池、支付 Chromium 窗口、扩展加载、代理切换，以及每次操作确认弹窗中单独勾选的可选自动确认（默认关闭，后端要求二次确认）；
- Team 邀请/退出、K12 邀请与 Session 相关操作；
- TXT、Session JSON、CPA、sub2api、Cockpit、9router、Codex、AxonHub、Codex-Manager 等预览/导出。

执行联网任务前请先查看确认框、任务页和日志。

## Windows EXE 使用方法

### 1. 运行要求

- Windows 10 或 Windows 11（64 位）；
- [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)：用于显示 Wails 界面，较新的 Windows 通常已预装；
- Google Chrome 或兼容 Chromium：用于注册、登录、Session、支付窗口等浏览器任务；
- 对应业务所需的邮箱、代理、接码或支付配置。

WebView2 只负责显示主界面，**不能代替**自动化任务所需的 Chrome/Chromium。界面空白或无法启动时先修复/安装 WebView2；任务提示无法启动 Chromium 时，先安装 Chrome，或在启动 EXE 前指定：

```powershell
$env:CHROME_BIN = 'C:\Program Files\Google\Chrome\Application\chrome.exe'
& '.\openai-register.exe'
```

未设置 `CHROME_BIN` 时，程序会查找常见 Chrome 安装位置；仍未找到时，浏览器库可能尝试下载 Chromium，因此需要可用网络。

### 2. 首次启动

1. 按下文“开发、验证与构建”生成 `build\bin\openai-register.exe`，或从可信的项目 Release 取得对应版本，放到任意普通目录。它是便携版，无需安装；不要放进需要管理员权限才能写入的系统目录。
2. 双击 EXE，或在 PowerShell 中执行 `& '.\openai-register.exe'`。
3. 先核对窗口显示的状态文件路径。默认是 `%APPDATA%\OpenAIRegister\state.json`。
4. 在“账户工作台”导入账号，并在“设置”“邮箱”“手机与接码”“代理”中填写实际配置。
5. 如需支付扩展，在“支付资料”或“代理”页手动选择扩展目录。
6. 先用少量账号检查任务日志；确认配置和路由符合预期后再提高并发。
7. 要删除账号时，在右侧“选择账户”中选中一项或多项，点击底部红色“删除所选”，核对确认框中的数量和邮箱后再确认；任务运行中不能删除。

EXE 可以移动或复制到其他机器，但状态文件默认不跟随 EXE 移动。首次产生保存内容后，程序会在用户配置目录创建 `state.json` 和 `state_data`；如需连同数据迁移，必须单独复制状态副本并用下述环境变量指向它。

## 状态文件与环境变量

默认存储位置：

```text
%APPDATA%\OpenAIRegister\state.json
%APPDATA%\OpenAIRegister\state_data\
%APPDATA%\OpenAIRegister\state_data\sessions\
```

`state.json` 保存账号索引、设置和 Session 索引；完整 Session 按账号拆分到 `state_data\sessions`。这些文件可能包含邮箱口令、Token、浏览器状态、代理凭据和支付资料，请按敏感数据保护，不要提交到 Git、网盘公开链接或问题报告。

程序启动时读取以下环境变量：

| 变量 | 作用 | 优先级/默认值 |
|---|---|---|
| `STATE_FILE` | 指定 `state.json` 的完整路径 | 最高；未设置时使用 `OPENAI_REGISTER_HOME\state.json` |
| `STATE_DATA_DIR` | 指定拆分数据目录 | 未设置时为最终 `STATE_FILE` 同目录下的 `state_data` |
| `OPENAI_REGISTER_HOME` | 整体指定默认数据根目录 | 仅在未设置 `STATE_FILE` 时生效 |

使用独立数据目录：

```powershell
$env:OPENAI_REGISTER_HOME = 'D:\OpenAIRegisterData'
& '.\openai-register.exe'
```

直接指定旧数据或测试副本：

```powershell
$env:STATE_FILE = 'D:\OpenAIRegisterData\state.json'
$env:STATE_DATA_DIR = 'D:\OpenAIRegisterData\state_data'
& '.\openai-register.exe'
```

这些 PowerShell 环境变量只对当前终端及其启动的程序生效。不要让 Python 旧版和 Go 新版同时写入同一组状态文件。

## 从 Python 旧版迁移

迁移前先退出 Python 旧版和 Go 新版，并备份完整数据。不要只备份新版 `state.json`：拆分后的 Session 实体在 `state_data` 中。

必须使用 Python 数据的**副本**迁移，不要直接让 Go 版首次启动就修改唯一一份旧数据。推荐流程：

1. 复制旧版的 `state.json`；如果存在 `state_data`，也要把整个目录一起复制到一个新的备份位置。
2. 选择一种迁移方式：
   - 把副本放到 `%APPDATA%\OpenAIRegister\`，然后正常启动 EXE；
   - 保留副本在自定义目录，并用 `STATE_FILE`、`STATE_DATA_DIR` 指向它。
3. 启动后先检查账户数、Session 数和全局日志，不要立即运行联网批量任务。
4. 确认迁移正常后，再把这组 Go 数据作为日常数据使用。

读取旧版单体 `state.json` 时，Go 版会自动迁移为轻量索引和拆分 Session，并在同目录创建：

```text
state.backup-before-state-split-YYYYMMDD-HHMMSS.json
```

这个自动文件只保护“旧单体状态首次拆分”的场景，不能代替日常备份。恢复时应先退出程序，并把同一时间点备份的 `state.json` 与 `state_data` 成对恢复；不要在程序运行时手工编辑或替换它们。

## 支付扩展

发布版的支付扩展目录默认为空，不包含开发机路径，也不会自动信任或安装任何扩展。

1. 从可信来源取得扩展并先解压。
2. 选择 **直接包含 `manifest.json` 的目录**，不要选择 ZIP 文件，也不要选择它的上一级目录。
3. 在“支付资料”或“代理”页点击“选择目录”，保存后再打开支付窗口。
4. 如果移动或重新解压了扩展，需要重新选择目录。

解压扩展能读取其获准访问的网页和填入资料，应像可执行程序一样审查来源。支付窗口可访问真实 checkout；“自动确认”默认关闭，且不会保存为下次操作的默认值。每次打开支付窗口时，必须在当次确认弹窗中单独勾选自动确认，再点击“确认执行”；它可能完成真实订阅或扣款。即使前端已勾选，后端仍会校验独立的自动扣款确认字段，缺少二次确认就拒绝执行。

## 危险操作与确认

确认框不是演示提示。点击“确认执行”表示允许程序发起对应的真实操作。重点包括：

- 删除/清空账号、分组、手机号池，或清理代理池；
- 注册租号、代理预检/预热等会消耗余额或流量的操作；
- 创建 checkout、打开支付窗口和自动确认支付；
- Team 邀请新增计费席位、退出 Team、K12/试用相关请求；
- 生成新的 Cloud Mail Token（旧 Token 会失效）。

执行前核对确认框中的账号数量、邮箱预览、代理模式和费用说明。自动确认支付默认关闭；如确实需要，必须在每次操作的确认弹窗中单独勾选，并再次点击“确认执行”。该选择只对当次操作生效，且后端会二次校验自动扣款确认字段。取消任务或关闭程序只能阻止尚未执行的步骤，不能撤销已在远端创建的账号、席位、checkout、Token 或支付。

## 源码结构

```text
main.go                 Wails 桌面应用入口
frontend/               Svelte 界面、页面、组件和 TypeScript 绑定
internal/ui/            Wails API、任务协调、事件和安全确认边界
internal/state/         兼容 Python 的原子状态存储与迁移
internal/worker/        注册、登录、Session 和浏览器工作流
internal/browser/       Chromium 启动、指纹、存储状态和页面交互
internal/tlsclient/     Chrome TLS/HTTP2 指纹客户端
internal/mail/          IMAP 与 Cloud Mail
internal/phoneprovider/ 接码提供者
internal/proxy*/        代理链、路由、健康检查和提供商代理池
internal/paymentwindow/ 隔离的支付浏览器窗口
internal/export/        预览、转换和导出
build/                  Windows 清单、图标和本地构建产物（bin 不提交）
```

## 开发、验证与构建

开发构建需要 Go 1.26、Node.js 22 LTS/npm，以及 Wails v2.12.0。先安装锁定依赖并验证前端：

```powershell
Set-Location '.\frontend'
npm ci
npm run check
npm test -- --run
npm run build
```

修改 Go 侧公开绑定后，先在仓库根目录重新生成 Wails TypeScript 绑定：

```powershell
go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 generate module
```

再回到仓库根目录验证 Go：

```powershell
Set-Location '..'
go build ./...
go test ./... -count=1
go vet ./...
```

构建 Windows EXE：

```powershell
go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 build -o openai-register.exe
```

默认产物位于：

```text
build\bin\openai-register.exe
```

## 验证边界

仓库内的自动化验证只使用临时状态、fake 服务和离线数据，覆盖前后端构建、单元/集成逻辑、状态迁移、安全确认和主要任务编排。验证过程不会读取你的真实状态文件，也不会主动测试真实注册、邮箱、付费代理、租号、Team/K12 邀请、checkout 或支付。

外部服务的凭据、额度、风控和页面结构随实际环境变化，无法由离线测试替代。首次使用真实配置时，请先备份状态，从只读探测或单账号、低并发、小批量开始，逐项核对日志和远端结果；涉及付费或不可逆操作时必须再次核对确认框后再执行。

## 开源许可与安全报告

本项目采用 [MIT License](LICENSE)。公开仓库不包含任何运行状态、账号、Token、
代理凭据、支付资料、H 盘迁移副本或预编译 EXE。提交安全问题和脱敏要求请先阅读
[SECURITY.md](SECURITY.md)。公开源码也不会附带私人 Cloud Mail 服务地址、邮箱域名、
K12 Workspace ID 或代理供应商账号；这些功能必须使用你有权使用的配置。域名邮箱的
公开默认值是保留示例域名，需要自托管时请修改 `DefaultDomainMailDomain` 后重新构建。
