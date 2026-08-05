# UI_SPEC — Wails port of the Tkinter app

Source of truth: the legacy Python/Tkinter `app.py`, `class App` (lines 12299–24805, 457 methods),
distilled from 9 independent region maps. Backend: this repository
(`docs/API_SURFACE.md`, `internal/worker/run.go`).

**Scope of what already exists in Go:** the *mechanical* layer is done — browser driving, register/login
flows, payment-link synthesis, mail, SMSBower client, proxy chaining/health, state persistence, models.
**The entire application layer is not.** `internal/ui/app.go` today exposes exactly four methods
(`Startup`, `Log`, `Environment`, `LoadSummary`). Every screen below needs bindings that do not exist.
Section 5 is the real work item.

Counts: **28 in-app screens/panels/dialogs + 3 external browser surfaces**, **~115 user actions**,
**60 persisted settings keys**.

---

## 0. Global structural facts (read before anything else)

1. **There is no tab bar.** The Tk code does `style.layout("Workspace.TNotebook.Tab", [])`, which
   deletes the tab strip. Region map A describes a "7-tab notebook"; map H proves those tabs are
   invisible. **Resolution: the app is a left sidebar + routed panes.** Do not build a tab bar.
2. **Sidebar has 9 entries mapping onto 7 host panes.** `team`, `k12` and `actions` all resolve to the
   same 全部操作 pane; `team` additionally selects nested action sub-tab index 4, `k12` index 5.
3. **Row identity is a bug to fix, not port.** Tk uses `iid = str(index_into_self.accounts)`. Every
   handler does `int(iid)`. Any re-render, sort or filter invalidates selection identity.
   **Use lowercased email as the stable key everywhere.**
4. **The right-hand 选择账户 dock (320px) is always visible on every page** and is a second, mirrored
   view of the same account list. Selection is bidirectional and guarded by a re-entrancy flag
   (`syncing_global_account_list`).
5. **Proxy pools are stored in Text widgets, not in a model.** That is the only reason the
   `take-auth-proxy` cross-thread RPC exists. In Go make the four pools a mutex-guarded struct and the
   RPC disappears.
6. **Colour system** (port to CSS variables): surface `#f5f7fa`, panel `#ffffff`, border `#d7dde5`,
   text `#1f2937`, muted `#667085`, primary `#2563eb`/hover `#1d4ed8`, danger `#b42318`/hover `#912018`,
   toolbar `#e9eef5`, **dark nav sidebar `#263238`** (hover `#37474f`, pressed `#455a64`, selected =
   white bg + `#1f2937` text), taskbar `#e6ebf1`, table row height 28, selection `#dbeafe` on `#111827`,
   headings `#eef2f6`/`#344054`. Base font Microsoft YaHei 10; log panes Cascadia Mono.
7. **Every button in the Tk app carries a Chinese tooltip.** Those tooltips are the de-facto product
   documentation and are reused verbatim as the 用途 column when an action group renders as a table.
   Carry them over as `title=`/help text — they are listed in the action catalogue's Effect column.

---

## 1. Screen inventory

### 1.1 Shell

| # | Screen | Controls / columns |
|---|---|---|
| S1 | **Root shell** | Title `OpenAI 注册 + Session 获取`, 1360×860, min 1040×680. Layout: sidebar (142px fixed) + content + right dock (320px). Tk also has a hidden 1×1 focus-sink label used to blackhole focus after every page switch — **drop it**, the web has no equivalent problem. |
| S2 | **工作区 sidebar** | Title `工作区`; nav buttons in order: 账户工作台(`workbench`), 邮箱(`mail`), 手机与接码(`phone`), 代理(`proxy`), 支付资料(`payment`), Team(`team`), K12(`k12`), 全部操作(`actions`), 设置(`settings`). Separator, then a wrapping muted label bound to `account_summary_var` (`账户 0 · 显示 0 · 已选 0`). |
| S3 | **当前选择 context strip** | Label `当前选择`; text from `selection_summary_var` (`未选择账户`); buttons `定位`, `×`. |
| S4 | **选择账户 dock (right, 320px)** | Header + summary (`账户 0 · 已选 0`); search Entry + `×`; **Treeview: 邮箱(188px) / 类型 · 状态(108px)**, `selectmode=extended`, drag-range select; footer `全选结果` (left) / `清空选择` (right). |
| S5 | **Taskbar (bottom)** | Label from `task_summary_var` (`当前任务：空闲`); right button `查看日志` (jumps to workbench + log tab). |

### 1.2 账户工作台 (workbench)

| # | Screen | Controls / columns |
|---|---|---|
| S6 | **常用操作 toolbar** | `导入账号`, `注册取 Session` (primary), `刷新 Session`, `检测试用`, `批量提链`, `导出 ZIP`, right-aligned `停止` (danger). |
| S7 | **任务参数** | `支付模式` readonly combo (14 `PAYMENT_MODES` keys, default `无卡长链接 US/USD`); `目标金额` Entry(9); `提链重试` Spin 1–10000 (3); `认证并发` Spin 1–30 (10); `无头浏览器` check; `导出名前缀` Entry(18). |
| S8 | **Account pane header** | `分组` readonly combo (全部, 未分组, …); `状态` readonly combo (全部状态 / 待处理 / 有 Session / Plus / Team / 提链成功 / 失败); right Menubutton `分组管理` → 新建分组 / 重命名当前分组 / 删除当前分组; `搜索` Entry + `×` (120 ms debounce); a `可用操作` strip repopulated dynamically per selection (context action buttons + overflow menu). |
| S9 | **Account table** | **Columns: 邮箱(260, stretch) / 类型(72) / 状态(160, stretch) / 次数(70, centred)**, `selectmode=extended`, height 14, sortable headings (custom→asc→desc per column). Right-click context menu (S27). Middle-drag = manual reorder → sets sort to CUSTOM. **`状态` is DERIVED, not stored** — see §1.6. |
| S10 | **Detail → 结果概览** | Section 支付链接 + selection summary; buttons `复制长链接`, `提链代理打开`, `新代理打开`, `批量打开选中`; Entry ← `link_var`; row `长链使用代理` Entry ← `link_proxy_var` + `复制代理`. Section 流程状态 + `清空流程`; **workflow Treeview: 步骤 / 状态 / 说明**, height = len(WORKFLOW_STEPS)=7, non-selectable, tags `wf_success #15803d`, `wf_failed #b91c1c`, `wf_running #1d4ed8`, `wf_manual #b45309`. |
| S11 | **Detail → Session** | `复制 Access Token`, `复制 Session JSON`; read-only text (height 10). Rendered sections in fixed order: Session Summary (Plan / Session Plan / Backend Plan / Account Tail / Token Expires / Backend Check Error), Saved Browser Fingerprint + UA, Access Token, Checkout URL, Payment Link Type, Amount Check, K12, Team Invite, Plus Trial, OpenAI Deactivation Mail, Long Link Proxy + create/followup/approve proxies and exits, Session JSON. |
| S12 | **Detail → 邮箱** | Buttons `邮箱管理`, `查封禁邮件`, `手动登录取码`; read-only inspector text. |
| S13 | **Detail → 日志** | Horizontal split. Left: `选中账户日志：<email>` + per-account log. Right: `全局日志`. Cascadia Mono. Tags `log_error #b91c1c`, `log_success #15803d`, `log_attention #1d4ed8`. |

### 1.3 Config pages

| # | Screen | Controls / columns |
|---|---|---|
| S14 | **邮箱 / 导入邮箱** | Hint: `格式：email----password----client_id----refresh_token；域名转发追加 ----receive_mailbox=接收主邮箱`; `从文件导入`. Row: `域名分邮箱固定后缀：@mail.example.com`. Cloud Mail row: check `Cloud Mail API`, `Base` Entry(43) (default `https://cloud-mail.example.com`), `Token` Entry(28, masked), buttons `生成Token` / `测试` / `保存`. Import textarea (height 8). |
| S15 | **手机与接码** | LabelFrame **SMSBower 自动接码**: check `启用`, `API Key`(masked,32), `服务代码`(7, `dr`), `国家 ID`(7, `33`), `最高单价（建议 0.07；留空不限）`(9), buttons `测试余额` / `保存设置`. LabelFrame **Turnstile Solver（协议过盾，可选）**: check `启用`, `服务 URL`(42, `http://127.0.0.1:8888`), `测试连接`. Then `每个手机号最多接码次数（0=不限制）` Entry(8), phone textarea (h3) + vertical buttons `导入手机号` / `重置手机号` / `清空手机号` / `手动取码`. **Phone Treeview: 手机号(180) / 接码次数(80) / 状态(120) / 最近验证码(120)**, height 3. |
| S16 | **支付资料 / PayPal扩展** | Hint `支付 PP 用；这里的手机号不是授权接码手机号`. `PP手机号` Entry(24), `卡信息` Entry (expand), `保存`. `支付链接扩展目录` Entry(72) + `选择目录`. Card format hint `卡号----有效期----CVV----电话----sms-token----姓名----街道,城市 邮编,国家`. `PP取码链接` Entry. PP phone-pool textarea (h3, `+手机号----https://接码链接`, first line consumed then removed). Card-pool textarea (h3, `卡号\|月\|年\|CVV…`) + `导入卡` / `重置卡`. **Card Treeview: 卡号(220) / 有效期(100) / CVV(80) / 状态(80)**, height 3. |
| S17 | **代理设置** | Header `链式：本地代理 -> 动态代理 -> 目标站点`. `本地代理` Entry(36, `http://127.0.0.1:7890`); `代理模式` readonly combo (`照旧` \| `全走本地代理`); hint `本地留空=直连；全走本地代理会忽略所有动态/支付/提供商代理池`. Horizontal split — **LEFT 手工代理池**: four textareas (h3) with live-count titles `注册/获取 Session 动态代理池（剩余 N）`, `创建长链第一步代理池（剩余 N）`, `创建长链后续代理池（剩余 N）`, `Approve 代理池（剩余 N）`. **RIGHT 代理提供商配置（后台预检池）**: Treeview **阶段 / 启用 / 主机:端口 / 地区 / 状态**, one row per role (第一步 / 后续 / Approve), double-click edits; buttons `编辑选中阶段`, `应用配置并预热`; note `各启用池达到 200 后开跑；降到 200 自动补至 500`. Below: `撞链代理地区` readonly combo (`自动(跟随支付地区)`, `不限`, + 21 `"CC 中文名"` entries); `单账号撞链并发数` Spin 1–30; `预检上限/池` Spin 1–10000 (500); `预检并发` Spin 1–300 (100); `预检支付代理池`; `清理无效代理`; reuse Entries `第一步复用代理` / `后续复用代理` / `Approve 复用代理` (72 each); checks `提取长链强制日本出口（不勾选=只记录出口，不限制）`, `注册时使用支付链接动态代理（特殊情况勾选；不勾选则用上方动态代理池）`, `旧版强撞 PayPal（忽略 checkout 支付方式列表，直接尝试 PayPal confirm）`; **second copy** of `支付链接扩展目录` + `选择目录` (same var as S16). |
| S18 | **设置 / 提示音** | check `成功提示音` (default on), check `长链成功后暂停其他账户` (default on), `输出设备` readonly combo(72) (`系统默认`), buttons `刷新设备`, `测试提示音`. |
| S19 | **全部操作** | 6 sub-groups. **Rendering rule (`_button_grid`): ≤4 items → uniform button grid; >4 items → Treeview 操作(150) / 用途(560)** with height `min(6, max(3, n))`, a primary `执行选中操作`, double-click to run, first row auto-selected. Groups: 账号(11→table), 注册Session(12→table), 支付链接(8→table), 导出转换(8→table), Team(3→buttons), K12(4→buttons). Extras: 注册Session has check `手动输入邮箱验证码（不自动调用邮箱令牌/IMAP）`; 导出转换 has `转换格式` combo (`sub2api`\|`cpa`\|`cockpit`\|`9router`\|`codex`\|`axonhub`\|`codexmanager`); K12 has `Workspace ID` Entry(44, default `workspace-example`) and `并发` Spin 1–30 (1). |

### 1.4 Dialogs

| # | Dialog | Controls |
|---|---|---|
| S20 | **提供商代理配置 - 第一步\|后续\|Approve** | 560×300, Escape closes. check `启用此阶段`; Entries `用户名`, `密码`(masked), `主机:端口`, `国家代码`; Spin `会话时长 t` 1–120 (5); hint `国家代码可用英文逗号分隔，例如 JP,US,DE`; `保存` / `取消`. Saving does **not** prewarm — user must press 应用配置并预热. |
| S21 | **自动分类账号** | combo `分类方式` (试用资格→`trial` / 长链结果→`link` / 账号类型→`plan`) + live hint; combo `作用范围` (全部→`all` / 当前分组可见→`current` / 选中→`selected`) + live hint; note `说明：只移动账号分组，不删除账号，也不修改 token。`; `取消` / `开始分类`. |
| S22 | **邮箱管理 - {email}** | 1120×720, read-only. Header `当前邮箱: {email}`, optional `读取主邮箱: {alias-parent}`, right status label. Controls: `文件夹` combo, `最近` Spin 10–500 + `封`, `搜索` Entry (Enter refreshes). Split — LEFT **Treeview: 类型(60,c) / 时间(145) / 发件人(180) / 标题(260) / 摘要(320)**; RIGHT buttons `读取正文` / `复制正文` / `复制验证码`, label `当前窗口只读，不会删除或移动邮件`, body text. Bottom: `刷新文件夹` / `刷新邮件` / `只看OpenAI` / `关闭`. Double-click a row = read body. |
| S23 | **填入 Session** (modal) | 820×620. `当前邮箱: {email}`; hint `粘贴 ChatGPT /api/auth/session JSON、导出的 session_json，或单独 Access Token`; `套餐覆盖` readonly combo `[auto, plus, free, team, k12, pro]` + hint `auto=按粘贴内容自动判断；如果你确认网页是 Plus，可选 plus`; button bar **duplicated above and below** the textarea: `确认保存Session` / `取消`; textarea h18; Ctrl+Enter submits. **MERGES** into the existing session payload. |
| S24 | **粘贴 Session JSON** (non-modal) | 720×420. Textarea h16; `保存Session` / `取消`. **REPLACES** — creates a synthetic account `pasted-session-YYYYMMDD-HHMMSS` (type free, status `Session已获取`). Despite the enclosing method name it does **not** generate a link. |
| S25 | **导出预览** (modal) | 760×520. Hint `请先核对导出内容，可复制；点击“确定导出”后选择保存文件。`; textarea h24 pre-filled; `复制内容` (left), `取消` / `确定导出` (right). Titles: 导出已授权邮箱 / 导出邮箱----RT / 导出选中Session(.json) / 导出 Session 转换 {label}(.json) / 导出选中Raw. |
| S26 | **Prompt 输入** | Raised from the event pump for manual email-OTP / manual phone entry. Blocking request/reply — see §4. |
| S27 | **Account context menu** | Cascade `设置类型` → Free / Plus / Team; cascade `移动到分组` → one item per group. |
| S28 | **Phone-code popup** | Info box showing a received SMS/email code. **Known defect: the manual-login-code worker passes the *email* where every other producer passes the *phone number*.** Fix in the port — give the event a typed `kind`. |

### 1.5 External browser surfaces (not webview)

| # | Surface | Notes |
|---|---|---|
| X1 | **Payment window** | Headed Chromium, fresh temp profile `paylink-profile-*`, payment autofill extension via `--load-extension`, autofill/password-manager disabled through a seeded Preferences file, full fingerprint spoof. Seeds PayPal phone/card/smsUrl into `localStorage` (`opencode_paypal_*`, `ppaf_*`). **Auto-clicks the OpenAI confirm/subscribe button and PayPal signup/continue — it can complete a real charge unattended, then marks the account Plus.** |
| X2 | **Trial page** | Headed Chromium restoring storage state, navigates `https://chatgpt.com/?promo_campaign=plus-1-month-free#pricing`, then holds the thread alive while the window is open. User is told to press 切换支付代理 before clicking the page button. |
| X3 | **Kept register / manual-OAuth browsers** | Parked per lowercased email, outlive the task, only reaped at app exit. Go equivalent: `worker.ParkBrowser` / `TakeParkedBrowser` / `CloseParkedBrowser` (already exists). |

### 1.6 Derived account-table 状态 (must be reimplemented exactly)

`_account_status_text(account)`:
1. `长链已提取` if `results[email]` is non-empty;
2. else `account.status` if set;
3. else `Session已获取` if the session payload has `access_token` or `session_json`;
4. else `待处理`;
5. overlaid with `K12请求成功` when the session's `k12_status` is 2xx;
6. overlaid with `待获取RT(带授权手机号)` when an auth phone is present and `openai_rt` is empty.

Full status vocabulary written by workers (needed for the 状态 filter and for colour coding):
处理中, Session已获取, 已登录, 需要手机号, 代理耗尽, 授权中, 授权失败, 授权代理耗尽, 协议注册中,
协议Session已获取, 协议注册失败, 协议代理耗尽, 协议需手机号(未接码), 登录中, 已登录, 需手动登录,
登录失败, 登录代理耗尽, 辅助登录中, 已填邮箱, 辅助登录失败, OAuth登录完成, OAuth需手动, OAuth已保留,
OAuth登录失败, 手动登录取码中, 验证码已弹出, 手动取码失败, 手动OAuth中/停止/超时/失败,
Free/Plus/Team/K12/Pro RT已获取, RT已获取, Team注册中, Team RT已获取, Team失败…, 域名邮箱注册取RT中,
域名邮箱待注册, Cloud Mail未配置, 重新获取中, 成功, K12一键注册中, K12请求中, K12请求成功, K12失败,
K12接受中, K12接受已刷新, K12接受失败, 刷新Session失败, 查封禁中, 疑似已封禁, 未见封禁邮件,
封禁检查失败, 打开试用页中, 试用页已打开, 打开支付页失败, 生成ApplePay页中, 提取PP链中, 提取GoPay链中,
ApplePay页已生成, 长链已提取, 提取长链失败, Session已手动填入, 已绑定手机号, Team待注册, `ACCOUNT_EMAIL_LOCKED_STATUS`.

Reserved group names: `未分组` (default), `全部` (filter-all), `邮箱锁定`, `域名邮箱主`, `域名邮箱分`.

---

## 2. Action catalogue

`Go call` = existing symbol, or **MISSING** (nothing in the backend does this).
`Blk`: `sync` = returns immediately on the UI thread · `task` = long-running goroutine with events ·
`rpc` = short async round-trip.
`$` = spends real money.

### A. Navigation & selection — all local, no backend

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 1 | 工作区导航 ×9 | Route to page; persists `workspace_page`. `team`→action sub-tab 4, `k12`→5 | frontend | sync | |
| 2 | 定位 | Return to workbench, scroll table to selection | frontend | sync | |
| 3 | ×  / 清空选择 | Clear global selection, log `已清空邮箱列表选择` | frontend | sync | |
| 4 | 全选结果 | Select everything visible in the right dock | frontend | sync | |
| 5 | 右面板搜索 + × | Filter dock list (120 ms debounce) | frontend | sync | |
| 6 | 全选可见 | Select all rows under current group/filter; warns `当前分组没有可见邮箱` | frontend | sync | |
| 7 | 反选可见 | Invert visible selection | frontend | sync | |
| 8 | 全选有 Session | Select visible rows whose payload has a non-empty `access_token`; warns `当前列表没有可批量提取的 Access Token` | frontend | sync | |
| 9 | 分组 filter | Clear selection, re-render, persist | frontend | sync | |
| 10 | 状态 filter | Re-filter, persist | **MISSING** (derived-status predicate) | sync | |
| 11 | 搜索 + × | Debounced table filter | frontend | sync | |
| 12 | Column sort | custom→asc→desc per column; persists `account_sort_column`/`_direction` | **MISSING** (sort key fn) | sync | |
| 13 | Middle-drag reorder | Reorder visible rows, force sort=CUSTOM, save | frontend + `state.Save` | sync | |
| 14 | Rubber-band / ctrl / shift select | Multi-select with auto-scroll | frontend | sync | |

### B. Account CRUD & grouping

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 15 | 导入账号 | Parse textarea; merge by lowercased email **preserving** existing `account_type/status/openai_rt/auth_phone_*/receive_mailbox/mail_provider/group`; new rows get the current filter group (or 默认); lazily load the split session file | `models.ParseAccountLine` + `state.Store.LoadDeferredSession` + `Save` | sync | |
| 16 | 从文件导入 | Pick `*.txt`, replace textarea with UTF-8 content | `wailsruntime.OpenFileDialog` | sync | |
| 17 | 删除选中 | Confirm (preview ≤20 emails), delete accounts + their `results` + `link_attempt_counts` | **MISSING** binding | sync | |
| 18 | 清空列表 | **NO CONFIRMATION in Tk.** Wipes accounts, resets groups to `[默认]`, filter to 全部, clears results + attempt counts. Does *not* clear `session_results`. **Add a confirmation in the port.** | **MISSING** | sync | |
| 19 | 新建分组 | Prompt, validate (1–32 chars, not reserved, no dup) | **MISSING** | sync | |
| 20 | 重命名当前分组 | Rename + re-tag members | **MISSING** | sync | |
| 21 | 删除分组 | Delete; members fall back to `默认分组` | **MISSING** | sync | |
| 22 | 移动到分组 `<g>` | Set `.group` on selection | **MISSING** | sync | |
| 23 | 设置类型 Free/Plus/Team | plus→status `Plus`; team→`Team待注册`; **free→clears status AND `openai_rt`** (destructive) | **MISSING** | sync | |
| 24 | 自动分类 → 开始分类 | Reassign `.group` by trial / link / plan over all/current/selected. Never touches tokens | **MISSING** | sync | |
| 25 | 别名注册 | Ask 1..`MAX_PLUS_ALIASES_PER_MAILBOX`; generate unique `+<3–6 digits>` alias accounts cloned from each selected mother mailbox (needs `client_id`+`refresh_token`); 80 attempts per length. Local only | **MISSING** | sync | |
| 26 | 生成域名邮箱 | Cloud-Mail mode: N (1–500, default 10) random `@mail.example.com` accounts, `mail_provider=cloudmail`, generated password, status `域名邮箱待注册`, group `域名邮箱分`. Forwarding mode: clone from group `域名邮箱主` with a resolved `receive_mailbox`. Auto-falls-back to cloud mode and force-enables `cloud_mail_enabled` | **MISSING** | sync | |
| 27 | 刷新类型 | Single selection, requires `openai_rt` else `这个邮箱还没有 rt_token，请先 OAuth授权取RT`. Chain proxy → detect account type → may rotate `openai_rt`, sets status `Team`/`已绑定手机号`/`Free` | **MISSING** `DetectOpenAIAccountType` | task | |

### C. Auth / registration

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 28 | 注册取 Session / 注册并取Session | Full register-or-login + capture Session. Statuses 处理中→Session已获取 | `worker.Worker.Run` | task | **$** |
| 29 | 注册或登录 | Same, no session read, window kept open | `worker.Worker.RunAuthOnly` | task | **$** |
| 30 | 开始-全部 | Same over all accounts | `Run` ×N | task | **$** |
| 31 | 登录并保留 | One account. Rotates the **whole** login proxy pool; headful; browser parked on success *and* on any non-transport failure. Statuses 登录中/已登录/需手动登录/登录失败/登录代理耗尽 | `worker` register flow `LoginExisting` + `ParkBrowser` — **no binding** | task | |
| 32 | 辅助登录Session | Blocking instruction box, then open login page, pass Cloudflare (`allow_manual=True`), retry `fill email` 15×1 s, park browser. Statuses 辅助登录中/已填邮箱/辅助登录失败 | **MISSING** | task | |
| 33 | 打开OAuth链接 | Prompt for an external OAuth URL; warn if not `https://auth.openai.com/oauth/authorize`. Drives a headful browser ≤900 s (1 s tick, 20 s progress log) handling Bad Gateway refresh, redirect→`DEFAULT_REDIRECT_URI`, add-phone/SMS page, email-verification, email field, authorize button (`/Authorize\|授权\|允許\|允许\|Approve\|同意\|Continue\|继续\|続行/i`), generic 继续. **Never writes `openai_rt`.** | **MISSING** | task | |
| 34 | 手动登录取码 | Open `chatgpt.com` in a parked headful browser, then block ≤600 s on the mailbox for a code, popup on arrival | `mail.Reader.WaitForCode` + browser launch — **no binding** | task | |
| 35 | 手动 OAuth | Exactly one selection (`手动 OAuth 一次只能选中一个邮箱`). Headful, user logs in by hand, poll all pages 1 s up to 900 s for the redirect, exchange code→refresh_token, classify plan → `<Plan> RT已获取`. Browser always parked | `TeamSSOFlow.ExtractOAuthCallbackFromURL` + `ExchangeBrowserCodeForToken` — **no binding** | task | |
| 36 | OAuth授权取RT | Sequential per account, walks the dynamic proxy list from the round-robin cursor; only transport errors advance the proxy. Writes `openai_rt`, derives plan → `Free/Plus/Team/K12/Pro RT已获取`. `allow_manual_phone = NOT (smsbower enabled and key set)` | `TeamSSOFlow.AuthorizeRTFromBrowser` (exists, unbound); proxy failover **MISSING** | task | **$** |
| 37 | 协议注册取Session | **No browser.** Pure HTTP OAuth + email OTP. Retryable: 403/429/502/503/504, timeout/proxy/connection/tls/turnstile/cloudflare/风控/被拦截. **Never** retryable: 该账号进入密码登录页, 等待 OpenAI 邮箱验证码超时, emailotpvalidate请求失败, wrong_email_otp_code, 已取消, 短信验证码, add-phone. **Explicitly money-safe**: `phone_provider=None`, `allow_manual_phone=False`, reports `协议注册取Session需要手机号（未接码/未扣费），已跳过` | **MISSING** (whole protocol path) | task | |
| 38 | 域名邮箱随机取RT / Team随机注册 | Requires Cloud Mail enabled+token. Creates one random `@mail.example.com` cloudmail account, resets SMSBower attempt counters, picks a register proxy, registers + authorizes | `worker.Worker.RunRegisterAndAuthorizeRT` | task | **$** |
| 39 | 停止 | Set the global stop signal; close payment contexts; unblock every pending prompt with `""`; `running=False` | **MISSING** (task registry) | sync | |

### D. Session

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 40 | 刷新 Session | Uses stored `storage_state_json`, but **prefers the live state of a parked browser** if one exists. **Concurrency forced to 1.** Headful (headless flag ignored). Cross-checks the plan against `/backend-api/accounts/check/v4-2023-04-27`, `/backend-api/accounts/{id}/subscription`, `/backend-api/me`; logs an old-vs-new token fingerprint diff | `browser.ApplyStorageState` + `ExportStorageState` exist; refresh flow **MISSING** | task | |
| 41 | 刷新K12 | Same, but only for payloads with `k12_workspace_id` + 2xx `k12_status`; injects an `_account` cookie and `exchange_workspace_token=true&workspace_id=…&reason=setCurrentAccount`; concurrency = `k12_concurrency` | **MISSING** | task | |
| 42 | 填入Session → 确认保存Session | Parse pasted JSON/wrapper/bare token (`未从粘贴内容中解析到 accessToken` on failure); apply 套餐覆盖; **MERGE** into the payload (`access_token`, `session_json`, `access_summary`, `plan_type`, `chatgpt_plan_type`, `account_id`, `chatgpt_account_id`); set `account_type` when plan ∈ {free,plus,team,k12,pro}; status `Session已手动填入` | `opll.extractAccessTokenFromSessionText` is **unexported** — needs promoting; rest **MISSING** | sync | |
| 43 | 粘贴 Session → 保存Session | Same parse, but **REPLACE** into a brand-new synthetic account | **MISSING** | sync | |
| 44 | 复制 Access Token | Clipboard; warns `当前邮箱暂无 Access Token` | `wailsruntime.ClipboardSetText` | sync | |
| 45 | 复制 Session JSON | Clipboard; warns `当前邮箱暂无 Session JSON` | `wailsruntime.ClipboardSetText` | sync | |
| 46 | 检测试用 | **Sequential, single thread.** Creates a real OpenAI checkout and POSTs `api.stripe.com/v1/payment_pages/{cs}/init` to read the amount; eligible ⇔ amount 0. No card charged, but it **does** consume checkout sessions server-side | `opll.OpllCreateCheckout` exists; the Stripe-init read is **unexported** | task | |
| 47 | 查封禁邮件 | Sequential. Scans 90 days / ≤120 msgs per folder for OpenAI deactivation notices; status 疑似已封禁 / 未见封禁邮件 / 封禁检查失败 | `mail.Reader.ScanOpenAIDeactivationNotice` (exists, unbound) | task | |
| 48 | 清空流程 | Drop the `workflow` key from the selected payload | **MISSING** | sync | |

### E. Payment links

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 49 | Session 生成 | One account. Trial-short-link mode needs `storage_state_json` (`试用长链需要浏览器登录态 storage_state_json…`); otherwise needs `access_token` and a started provider pool (`提供商代理配置无效，无法启动长链接任务`) | `opll.GenerateOpll{Paypal,Gopay,Hosted}LongLink` / `PayLinkExtractor.ExtractTrialShortLinkByClick`; orchestration **MISSING** | task | **$** |
| 50 | 批量提链 / 批量生成 | Multi-account. Resets `link_attempt_counts[email]=0` per job, builds a proxy-triple queue, races up to `link_race_concurrency` per account, retries up to `link_attempt_limit` | orchestration **MISSING** | task | **$** |
| 51 | 重新获取 | Re-login on the register proxy, carry the state to the extract proxy, pull a fresh link | `worker.Worker.Relink` | task | **$** |
| 52 | 批量重新获取 | One goroutine per account (Tk: unbounded) | `Relink` ×N; pool **MISSING** | task | **$** |
| 53 | 复制长链接 | Clipboard; warns `暂无长链接` | `ClipboardSetText` | sync | |
| 54 | 复制代理 | Copies the 3-part 第一步/后续/Approve summary; warns `当前选中邮箱暂无长链使用代理` | frontend | sync | |
| 55 | 提链代理打开 | Opens the saved link using the **followup proxy recorded at generation time**; falls back followup→payment→register pool; substitutes the configured extract proxy if the stored one is just the local proxy | **MISSING** (payment window) | task | **$** |
| 56 | 新代理打开 | Opens with a **new** payment proxy + payment extension/profile | **MISSING** | task | **$** |
| 57 | 批量打开选中 | One independent payment window **per selected link**; acquires a PP phone + a card **per link** | **MISSING** | task | **$×N** |
| 58 | 切换支付代理 | Hot-swaps the live trial chain's upstream without closing the browser. Guards: `当前没有打开中的试用页窗口`, `当前为“全走本地代理”，无需切换到支付动态代理`, `当前没有已取用的支付链接动态代理，请重新打开试用页` | `proxychain.Server.SetDynamicProxy` (exists, unbound) | sync | |
| 59 | 支付模式 combo | Switches country/currency/gopay/trial/apple-pay flags; when 撞链代理地区 is `自动` logs the newly derived region | `models.PaymentModes` | sync | |

### F. Team

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 60 | 邀请成员 | **Exactly one** Team account as inviter; prompt for target email; builds/exchanges a Team-workspace access token, then sends the invite. Invite only — no auto-accept, no removal | **MISSING** `chatgpt_team_send_invite` | task | **$** (billable seat) |
| 61 | 退出 Team | Confirm, then leave with the member's own token. Owners blocked (403 → must be removed by an Owner) | **MISSING** `chatgpt_team_leave_workspace` | task | |
| 62 | 扫描邀请加入 | Per account: wait ≤120 s for a Team invite mail → if no saved login state, run a **full register/login worker** → accept the invite in a headful browser → switch workspace → refresh session → `account_type='team'` | `mail.Reader.WaitForTeamInvite` exists; accept/switch **MISSING** | task | **$** |

### G. K12

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 63 | 一键注册加入 | 注册或登录并获取 Session → 请求邀请 → 接受邀请 → 刷新 Session. Multi-select concurrent, capped by `k12_concurrency` and auto-lowered to the auth proxy pool size. Excludes `account_type=team` and accounts with neither Cloud Mail nor `client_id`/`refresh_token` | `worker.Run` + **MISSING** K12 steps | task | **$** |
| 64 | 请求邀请 | POST `{ChatGPTBaseURL}/backend-api/accounts/{workspace_id}/invites/request`. HTTP only. Needs `access_token` per account | **MISSING** `k12_request_workspace_invite` | task | |
| 65 | 接受并刷新 | Request → `WaitForLink("k12-invite", timeout=240)` → headful browser accepts + switches workspace → re-read `/api/auth/session` | `mail.Reader.WaitForLink` exists; accept/switch **MISSING** | task | |

### H. Proxy

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 66 | 代理模式 combo | `全走本地代理` stops the provider pool manager and makes **every** pool reader return empty, `_reuse_link_proxy_for_region` return `""`, and provider roles empty | **MISSING** (route-mode gate) | sync | |
| 67 | 撞链代理地区 combo | Persist immediately; drives region filtering for every link stage | **MISSING** (region filter/rewrite) | sync | |
| 68 | 编辑选中阶段 → 保存 | Write 6 fields back (duration clamped 1..120), refresh tree, save. Warns `请先选择要配置的代理阶段` | **MISSING** `ProxyProviderConfig` | sync | |
| 69 | 应用配置并预热 | Local-only mode → stop manager + `当前为“全走本地代理”，已忽略提供商代理池`. Else validate (with 强制日本出口 on, the 第一步 role may only list JP: `已启用“强制日本出口”，第一步提供商 region 只能填写 JP`), set max workers = precheck concurrency, configure the pool → **background prewarm to 500 checked exits per enabled role**. Each candidate opens a proxy chain and detects the exit IP | **MISSING** `ProviderProxyPoolManager` | task | **$** (burns provider quota) |
| 70 | 预检支付代理池 | Concurrently probe ≤`link_proxy_precheck_limit` proxies per pool at `link_proxy_precheck_concurrency`. With 强制日本出口, non-JP create-pool exits count as failed. Failures skipped **for this round only** | `proxyhealth.DetectProxyHealthWithRetry` exists; orchestration **MISSING** | task | |
| 71 | 清理无效代理 | Dedupe across all 4 pools, confirm `将低并发检测 N 个唯一代理。每条连续两次无法连接 Auth/ChatGPT 才会删除，403 不会删除。是否开始？`, max 4 workers, 2 attempts, 0.6 s apart; removal is applied to **all four** pools + `save_state(flush=True)` | `proxyhealth.DetectLocalProxyHealthWithRetry` exists; orchestration **MISSING** | task | |
| 72 | 4 pool textareas | Editing rewrites the pool and refreshes the `（剩余 N）` titles (30 ms debounce) | **MISSING** `ParseProxyPoolText` | sync | |
| 73 | reuse proxy entries ×3 | Fixed per-stage overrides; approve keeps its original region, others get rewritten; `""` forces pool selection | **MISSING** | sync | |
| 74 | 3 behaviour checks | 强制日本出口 / 注册时使用支付链接动态代理 / 旧版强撞 PayPal (persists immediately) | settings only | sync | |

### I. Mail

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 75 | 生成Token | Confirm `生成新 Cloud Mail Token 会使旧 Token 失效`, prompt admin email/password, generate; on success set token + force-enable. **Destructive: invalidates the previous token** | `mail.CloudMailGenerateToken` | task | |
| 76 | 测试 (Cloud Mail) | `ListMails` on probe address `codex-healthcheck-<ts>@domain`, size 1 | `mail.CloudMailClient.ListMails` | task | |
| 77 | 保存 (Cloud Mail) | Validate `^https?://`, token required when enabled; force domain to `mail.example.com`; rewrite `mail_provider=cloudmail` + base/token on every matching account and strip it from non-matching ones | **MISSING** (`_apply_cloud_mail_runtime_config`) | sync | |
| 78 | 邮箱管理 | Open S22; needs Cloud Mail or Outlook OAuth. All I/O threaded, read-only | `mail.CreateMailReader` | rpc | |
| 79 | 刷新文件夹 | List folders | `Reader.ListFolders` | rpc | |
| 80 | 刷新邮件 / 只看OpenAI | Recent messages (10–500), optional search / forced `query="openai"` | `Reader.ListRecentMessages` | rpc | |
| 81 | 读取正文 | Fetch full body | `Reader.FetchMessage` | rpc | |
| 82 | 复制正文 | Clipboard | `ClipboardSetText` | sync | |
| 83 | 复制验证码 | Copy the detected code, falling back to extracting from the body | `mail.extractOpenAICode` is **unexported** | sync | |

### J. Phone / SMS

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 84 | 导入手机号 | Parse lines, dedupe by number, update `sms_url`, revive 不可用→可用; per-line errors logged as `第 N 行` | `models.ParsePhoneLine` | sync | |
| 85 | 重置手机号 | status=可用, clear `last_error`, `receive_count=0` | frontend + `Save` | sync | |
| 86 | 清空手机号 | Blocked while running; confirm with count | frontend + `Save` | sync | |
| 87 | 手动取码 | Poll the selected phone's SMS URL for ≤30 s, popup the code. Reads an already-rented number — rents nothing | `smsbower.Client.WaitForCode` (for smsbower://) / manual URL poll **MISSING** | task | |
| 88 | 测试余额 | Validate + save, then query the balance. Read-only | `smsbower.Client.GetBalance` | task | |
| 89 | 保存设置 (SMSBower) | Validate service `[A-Za-z0-9_]+`, numeric country, price >0 or empty, API key required when enabled | settings only | sync | |
| 90 | 测试连接 (Turnstile) | Probe `{url}/health`, `{url}/v1/health`, `{url}/` (timeout 8 s) | **MISSING** | task | |
| — | **(no button) SMSBower 自动取号/接码** | The money path. 5 actions: `next` (reuse the account's bound number, else SMSBower, else the first usable pool entry) · `code` (SMSBower `WaitForCode` 180 s, or poll the manual SMS URL ≤5 s apart, extracting 6 digits via `OpenAI[^\d]{0,80}(\d{6})` / 验证代码 / 验证码 / `\b(\d{6})\b`) · `sent` (status 1) · `good` (**status 6 — finalises the charge**) · `bad` (status 8 — releases; marks manual entries 不可用). `next` walks cheap→expensive price tiers, retrying once per tier, skipping on `NO_NUMBERS` / `MAX_PRICE_EXCEEDED` / 没有可用号码 / 超过最高限价. Returns `{number, sms_url:"smsbower://<id>", provider:"smsbower", activation_id, price, stock}` | `worker.PhoneProvider` **interface exists, no implementation** | — | **$** |

### K. Payment material

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 91 | 保存 (PayPal扩展) | Persist `paypal_phone` / `paypal_card` / `paypal_sms_url` / pool; log `PayPal 扩展资料已保存` | settings only | sync | |
| 92 | 选择目录 (×2 screens, same var) | Directory picker `选择解压后的 Chrome 扩展目录` | `wailsruntime.OpenDirectoryDialog` | sync | |
| 93 | 导入卡 | Parse `卡号\|月\|年\|CVV`, dedupe by card number preserving the old status on replace | `models.ParsePaymentCardLine` | sync | |
| 94 | 重置卡 | All statuses → 未用 | frontend + `Save` | sync | |
| — | **(internal) card + PP-phone consumption** | `_next_paypal_card_text`: first `未用` card → `replace_paypal_card_head(base, card)` → flip to 已用 → re-render → save → log `本次支付使用卡: {card}`. If the pool is non-empty but has no 未用 card: `支付卡池没有未用卡，请导入新卡或重置卡池` and **abort the whole action**. `_take_paypal_phone_config`: round-robin over the pool, advancing and persisting `paypal_phone_pool_index`; bad line → `PP手机号+接码池第 N 行格式错误` and abort | `models.ParsePayPalPhoneLine` exists; `replace_paypal_card_head` **MISSING** | sync | |

### L. Export

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 95 | 已授权 | Accounts with `openai_rt`; text = one `account_export_line(account, prefix)` per line. If any lack RT: `选中邮箱中有 N 个没有 RT，将先自动授权获取 RT 后再导出。…是否继续？` → runs authorize first, then resumes | **MISSING** | sync/task | |
| 96 | 邮箱 RT | Lines `{email}----{openai_rt}` | **MISSING** | sync | |
| 97 | sub2api | Save path **first**, then refresh every account's access token through one proxy chain (rotating `openai_rt` when a new one comes back), prefix emails as `({prefix}){email}` | **MISSING** `RefreshOpenAIAccessToken` + `build_sub2api_export` | task | |
| 98 | 选中 Session | JSON array of `{email, session_json}`; accounts without one are counted and logged only | **MISSING** | sync | |
| 99 | 选中 Raw | `account_export_line` for every selected account, no RT requirement | **MISSING** | sync | |
| 100 | 复制转换 | Convert to the chosen format → clipboard. **`cpa` first triggers a background RT refresh** and re-dispatches | **MISSING** (all 7 formats) | sync/task | |
| 101 | 导出转换 | Same, to a `.json` file via the preview dialog | **MISSING** | sync/task | |
| 102 | 导出 ZIP | **Skips the preview dialog.** One JSON per account into `session-conversion-{format}-{ts}.zip` (DEFLATE), entry names de-duplicated | **MISSING** | sync | |
| 103 | 预览: 复制内容 / 确定导出 / 取消 | **Known defect: 复制内容 copies the *edited* preview text, but the file written is the *original* string.** Fix in the port — write what the user sees | `ClipboardSetText`, `SaveFileDialog` | sync | |

### M. Settings & misc

| # | Label | Effect | Go call | Blk | $ |
|---|---|---|---|---|---|
| 104 | 成功提示音 | Persist | settings | sync | |
| 105 | 长链成功后暂停其他账户 | Persist. **When on, one account's link success sets the GLOBAL stop signal and cancels every other in-flight account** (`账号 X 长链已提取，已暂停其他账户继续尝试`) | settings + task registry | sync | |
| 106 | 输出设备 combo | Persist chosen audio device | **MISSING** | sync | |
| 107 | 刷新设备 | Enumerate output devices as `index: name / hostapi`, keep the previous selection by name; log `已刷新音频输出设备: N 个` | **MISSING** | sync | |
| 108 | 测试提示音 | 0.42 s 880 Hz + 1320 Hz tone on the selected device | **MISSING** | task | |
| 109 | 手动输入邮箱验证码 | Persist; changes every flow to prompt instead of polling | `worker.Config.ManualEmailOTP` | sync | |
| 110 | 查看日志 | Jump to workbench + log pane | frontend | sync | |
| 111 | 转换格式 combo | Persist `session_convert_format` | settings | sync | |
| 112 | K12 Workspace ID / 并发 | Persist | settings | sync | |
| 113 | Window close | Stop the provider pool, close every parked browser, `save_state(flush=True)`, destroy | `CloseParkedBrowser` + `state.Save(…, flush=true)` | sync | |
| 114 | Prompt 回答 | Answer a manual OTP/phone prompt, unblocking the worker | `worker.Config.InputCallback` (hook exists, registry **MISSING**) | rpc | |
| 115 | 执行选中操作 / row double-click | Dispatch the selected row of a >4-item action group | frontend | sync | |

---

## 3. Settings surface — 60 persisted keys

`state.json` top level: `updated_at`, `schema_version=2`, `accounts[]`, `phones[]`, `payment_cards[]`,
`results{email→url}`, `session_results{email→payload}` (on disk: a per-email index pointing at
`state_data/sessions/<sha256(lower(email))[:24]>.json`), `link_attempt_counts{email→int}`, `settings{…}`.

| Key | Owning control | Type / default / validation |
|---|---|---|
| `payment_mode` | S7 支付模式 | enum, `无卡长链接 US/USD`; validated against `PaymentModes`, remapped via `PAYMENT_MODE_ALIASES` |
| `target_amount` | S7 目标金额 | str, stripped |
| `headless` | S7 无头浏览器 | bool. **Ignored by 3 flows** (session refresh, K12 accept, trial link) which hard-code headful |
| `local_proxy` | S17 本地代理 | str, `http://127.0.0.1:7890` |
| `proxy_route_mode` | S17 代理模式 | enum `照旧`\|`全走本地代理`, default `照旧`; invalid→default on load **and** save |
| `dynamic_proxies` | S17 pool 1 | multiline (register/session pool) |
| `payment_dynamic_proxy` | S17 pool 2 | multiline (第一步/create) |
| `followup_dynamic_proxy` | S17 pool 3 | multiline (后续) |
| `approve_dynamic_proxy` | S17 pool 4 | multiline (Approve) |
| `reuse_payment_proxy` | S17 | str |
| `reuse_followup_proxy` | S17 | str; **if the key is absent on load, seeded from `reuse_payment_proxy`** |
| `reuse_approve_proxy` | S17 | str |
| `link_proxy_region` | S17 撞链代理地区 | enum: `自动(跟随支付地区)`, `不限`, + 21 `"CC 中文名"`; default `不限` |
| `require_japan_extract_proxy` | S17 | bool |
| `register_with_payment_proxy` | S17 | bool |
| `force_legacy_paypal` | S17 | bool |
| `auth_concurrency` | S7 认证并发 | int 1..30, default 10 |
| `k12_concurrency` | S19 K12 并发 | int, default 1 |
| `link_race_concurrency` | S17 单账号撞链并发数 | int 1..30, default 1 |
| `link_proxy_precheck_limit` | S17 预检上限/池 | int ≥1, default 500 |
| `link_proxy_precheck_concurrency` | S17 预检并发 | int ≤300, default 100 |
| `link_attempt_limit` | S7 提链重试 | int 1..10000, default 3 |
| `provider_proxy_configs` | S20 dialog | `{create\|followup\|approve: {enabled, username, password, endpoint, duration 1..120 (5), regions ("JP")}}` |
| `payment_extension_dir` | S16 **and** S17 (same var) | str，默认留空，由用户选择本机扩展目录 |
| `paypal_phone` | S16 | str |
| `paypal_card` | S16 | str (base card, head replaced per use) |
| `paypal_sms_url` | S16 | str |
| `paypal_phone_pool` | S16 textarea | multiline |
| `paypal_phone_pool_index` | (internal cursor) | int, round-robin |
| `export_name_prefix` | S7 导出名前缀 | str |
| `domain_mail_domain` | — | **Always written as the constant `mail.example.com` and forced back to it on load.** The runtime var is never persisted |
| `cloud_mail_enabled` | S14 | bool |
| `cloud_mail_base` | S14 | str, `https://cloud-mail.example.com`, `^https?://` |
| `cloud_mail_token` | S14 | str, required when enabled |
| `k12_workspace_id` | S19 | str, `workspace-example` |
| `session_convert_format` | S19 转换格式 | enum of 7, default `sub2api` |
| `phone_max_receive_count` | S15 | int ≥0, 0 = unlimited; a phone at/over the cap is frozen |
| `smsbower_enabled` | S15 | bool |
| `smsbower_api_key` | S15 | str, required when enabled |
| `smsbower_service` | S15 | str, `dr`, `[A-Za-z0-9_]+` |
| `smsbower_country` | S15 | str, `33`, digits |
| `smsbower_max_price` | S15 | str, `0.07`; empty = no cap; else >0 |
| `manual_email_otp` | S19 | bool |
| `turnstile_solver_enabled` | S15 | bool |
| `turnstile_solver_url` | S15 | str, `http://127.0.0.1:8888` |
| `success_sound_enabled` | S18 | bool, default true |
| `success_audio_device` | S18 | str, `系统默认` |
| `pause_others_on_link_success` | S18 | bool, default true |
| `account_groups` | S8 分组管理 | []str, default `["未分组"]` |
| `account_group_filter` | S8 | str, default `全部` |
| `account_status_filter` | S8 | enum of 7 |
| `workspace_page` | S2 | enum of 9 |
| `account_sort_column` | S9 | `email`\|`type`\|`status`\|`attempts`, default `email` |
| `account_sort_direction` | S9 | `custom`\|`asc`\|`desc`, default `custom` |
| `window_geometry` | window | str; **deliberately not persisted when maximized or edge-snapped**; restored clamped on-screen (floor 1040×680 / 800×600) |
| `window_zoomed` | window | bool |
| `ui_layout_version` | — | int, 4; sash ratios discarded when the saved version differs |
| `main_sash_ratio` | layout | float, clamp 0.2..0.85, default 0.27 |
| `log_sash_ratio` | layout | float, clamp 0.2..0.8, default 0.5 |
| `body_sash_ratio` | layout | float, clamp 0.2..0.8, default 0.43 |

**Layout keys** (`window_*`, `ui_layout_version`, `*_sash_ratio`) are Tk-specific. Keep the keys for
round-trip compatibility, but drive the web layout from CSS/localStorage; do not reimplement the
80 ms×8 sash-restore retry loop.

### Session payload keys (`session_results[email]`)

`access_token`, `session_json`, `storage_state_json`, `checkout_url`, `openai_rt`, `id_token`,
`link_proxy`(+`_label`,`_exit`), `link_create_proxy`(+`_label`,`_exit`), `link_followup_proxy`(+…),
`link_approve_proxy`(+…), `payment_link_type` (`apple_pay_hosted`\|`gopay_redirect`\|`paypal_approve`),
`stripe_amount`, `stripe_amount_source`, `target_amount`, `amount_check`,
`k12_workspace_id`, `k12_status`, `k12_response`, `k12_invite_url`, `k12_accept_result`, `k12_switch_result`,
`team_invite_url`, `team_workspace_id`, `team_accept_result`,
`access_summary` (dict), `plan_type`, `chatgpt_plan_type`, `account_id`, `chatgpt_account_id`,
`plus_trial_{eligible,status,amount,currency,country,amount_source,checked_at,detail}`,
`openai_deactivation_{found,status,checked_at,subject,date,folder,to,snippet}`,
`email_locked`, `email_locked_detail`, `email_locked_at`,
`workflow{step→{state, detail(≤500 chars), updated_at}}`.

`workflow` steps: `email`, `proxy`, `auth`, `session`, `trial`, `link`, `export`.
States: `未开始`, `进行中`, `成功`, `失败`, `需要人工`, `跳过`.

`worker.PayLinkResult` already matches the `link_*` / `payment_link_type` / amount subset exactly —
persist it field-for-field.

---

## 4. Concurrency contract

### 4.1 What the Python does (and why it must change)

> N unmanaged daemon threads → one unbounded, untyped `queue.Queue` → one Tk `after`-driven drain loop
> that owns all widgets, with a single global `threading.Event` as the only cancel signal.

- 59 `threading.Thread` sites, all daemon, all fire-and-forget, no registry, no join.
- One `self.events` queue carrying **29 positional tuple kinds** with no schema (`event[1]` is
  sometimes a string, sometimes a dict, once a live `queue.Queue`).
- The drain loop is budgeted at **300 events or 50 ms per tick**, rescheduled at 10 ms when backlogged
  and 100 ms when idle.
- Two handlers open **modal dialogs from inside the drain loop**, freezing all event processing.
- `finally: events.put(("done",))` appears **37 times** and is what resets `running` / the stop event /
  the task summary.

### 4.2 Required Go/Wails design

**Events: typed, one struct per name.** Replace the tuple queue with `runtime.EventsEmit`.

| Event | Payload | Notes |
|---|---|---|
| `log` | `{seq, ts, email?, message, level, module}` | `level` ∈ error/success/attention/normal; `module` ∈ 系统/代理/认证/邮箱/手机/Session/支付链接/支付窗口/导出 |
| `status` | `{email, status}` | **103 producers — the highest-frequency event.** Coalesce per email on the frontend |
| `account-updated` | `{email, account}` | |
| `result` | `{email, payload}` | The full `PayLinkResult` + amount fields |
| `link-attempt` | `{email, count}` | Increments the 次数 column |
| `session-result` | `{email, payload}` | Covers protocol-session / session-refresh |
| `phones-updated` / `cards-updated` | `{}` | Pool tables changed |
| `pools-updated` | `{register, create, followup, approve}` | Replaces the four `remove-*-proxy` events |
| `provider-proxy-status` | `{role, ready, target, checking}` | Renders `可用 N/500 检测中 M` |
| `task-started` / `task-finished` | `{taskId, kind, email?, err?}` | Replaces the 37 `("done",)` sites |
| `code-popup` | `{kind:"phone"\|"email", key, code}` | **Fixes the overloaded key defect (S28)** |
| `k12-result`, `team-invite-result`, `team-leave-result`, `trial-eligibility-result`, `deactivation-check-result`, `email-locked`, `mark-plus` | `{email, …}` | Direct ports |

**Log discipline.** The Tk version batches every log line arriving in one tick into a single insert plus
one scroll, and ring-buffers at **2000 records per view / 10000 total**. A naive per-line emit into the
webview will drown it. Batch on the Go side (flush every ~50 ms or 200 lines) and cap the frontend
buffers identically. Log routing: a leading `[email]` bracket or an explicit email field routes a line
to the per-account pane; everything else is global. Module prefix and severity are keyword-derived —
reproduce `_normalize_log_message` and `_log_record_tag` or account-scoped views break.

**Two events are request/reply, not fire-and-forget:**

1. **Manual input** — `worker.Config.InputCallback(kind, email, prompt) string` blocks the worker.
   Implement a `map[requestID]chan string` on the App, emit `prompt-request{id, kind, email, prompt}`,
   and expose a bound `AnswerPrompt(id, value string)`. **The Python version has no timeout** — add a
   context deadline. Cancelling a task must push `""` into every pending channel.
2. **Proxy checkout** — `take-auth-proxy` exists only because the pools live in Text widgets.
   **Delete it.** Make the pools a mutex-guarded Go struct; workers call `pool.Take(role)` directly and
   the App emits `pools-updated`.

**Cancellation.**

- Python has **one** global `threading.Event` for the whole app, cleared by whichever `("done",)`
  arrives first. With multiple payment windows open, the first `open-link-done` clears the stop signal
  for everyone — **a real bug.**
- Go: a `TaskRegistry` of `taskID → context.CancelFunc` with `CancelAll()` behind 停止. Per-account work
  inside a batch gets a child context so `pause_others_on_link_success` can cancel siblings without
  killing unrelated tasks.
- `worker.Run/RunAuthOnly/RunTeam/RunRegisterAndAuthorizeRT/Relink` already take a `ctx` — plumb it.
- **Playwright/go-rod calls are not interruptible mid-call.** Cancellation is cooperative: check
  `ctx.Err()` between steps, exactly as the Python polls `stop_event` between page operations.
- Parked browsers (`worker.ParkBrowser`) deliberately outlive their task. They are reaped only at app
  exit. Never call `ExportStorageState` on a parked browser from a synchronous binding — it blocks on
  browser IPC. Expose it as an async binding.

**Bounded concurrency.** Python spawns one OS thread per account with no cap, and each can spawn up to
`race_concurrency` (max 30) more. Use `errgroup` with `SetLimit`. Semantics to preserve exactly:
first success cancels that account's remaining attempts; a non-retryable error stops that account
entirely; failed manual-pool proxies go back to the **tail** of the queue; failed provider-pool proxies
are dropped. Note `_remove_failed_link_proxy_triple` is an explicit **no-op** — run failures never prune
persistent pools; only precheck does.

**Settings ownership.** Despite a `ui_thread_id` check on the logger, Python worker threads still read Tk
vars directly (`smsbower_enabled.get()` deep inside the register flow, `turnstile_solver_url.get()` in
the manual-OAuth worker, `local_proxy.get()` in dialog workers). **In Wails the frontend owns no
config.** Settings live in a Go struct behind an `RWMutex`; workers read that struct, never the UI.

**Persistence.** `state.Store` already implements the 1.5 s debounce, version-stale-drop, atomic write
and per-email dirty session files. Keep `Save(snapshot, dirtyEmails, flush)` and call `flush=true` only
on task completion and app exit. Nothing builds the snapshot map today — see gap G1.

---

## 5. Gaps — what the Go backend does not expose

Confirmed by symbol search across `internal/**/*.go`. **This is the work item.**

### 5.1 Blockers (nothing usable ships without these)

| ID | Gap | Detail |
|---|---|---|
| **G1** | **No settings model, no snapshot builder** | Zero of the 60 `settings.*` keys are typed. `state.Store.Save` takes a `map[string]any` and **nobody builds it**. Needs a `Settings` struct with the exact defaults/clamps/validators in §3, plus `ToSnapshot()`/`FromSnapshot()`. |
| **G2** | **No Wails bindings** | `internal/ui/app.go` has 4 methods. Every action in §2 needs one. |
| **G3** | **No `PhoneProvider` implementation** | The interface exists; nothing implements it. **Registration cannot complete phone verification without it.** Needs: SMSBower-backed provider (price-tier walk, `next/sent/code/good/bad`, per-email attempt counters under a mutex), manual-pool provider (SMS-URL polling with the 4 regex patterns, freeze at `phone_max_receive_count`), and the account-bound-number reuse rule (skip when the saved entry is 不可用/冻结/使用中, or when US is requested and the number is not `+1`). |
| **G4** | **No task registry / cancellation** | No `running` gate, no stop signal, no per-task contexts, no `task-started`/`task-finished`. |
| **G5** | **No proxy pool model** | Four pools + rotation (`Take` = pop head, append to tail) + removal + `（剩余 N）` counts + `ParseProxyPoolText` + an exported `NormalizeProxyURL` (currently unexported in `proxyhealth`). |
| **G6** | **No route-mode gate** | `全走本地代理` must make every pool reader return empty, reuse-proxy return `""`, provider roles empty, and stop the provider manager. Today nothing knows about it. |
| **G7** | **No batch orchestration** | `worker.Run` runs *one* account. Nothing does: work queue → bounded pool → per-account retry with a fresh proxy → `代理耗尽` on exhaustion → `errgroup`. Same for link generation (proxy-triple queue + racing attempts + retry classification via the existing `opll.OpllIsNonRetryableLinkError`). |
| **G8** | **No derived-status / sort / filter functions** | Table 状态 (§1.6), `_account_matches_status_filter`, `_account_sort_key`. |

### 5.2 Missing OpenAI API calls

| ID | Missing symbol | Used by |
|---|---|---|
| **G9** | `RefreshOpenAIAccessToken(rt, proxy)` | 刷新类型, sub2api export, CPA conversion refresh, Team token exchange |
| **G10** | `DetectOpenAIAccountType(rt, proxy)` | 刷新类型 |
| **G11** | `ChatGPTTeamSendInvite(token, accountID, email, proxy)` | 邀请成员 |
| **G12** | `ChatGPTTeamLeaveWorkspace(token, proxy)` | 退出 Team |
| **G13** | `K12RequestWorkspaceInvite(token, workspaceID, proxy)` | 请求邀请, 接受并刷新, 一键注册加入 |
| **G14** | K12/Team **invite accept + workspace switch** in a browser | 接受并刷新, 扫描邀请加入 (`playwright_click_invite_accept`, `playwright_switch_k12_workspace_via_profile`) |
| **G15** | `DetectPlusTrialEligibility(token, proxy, country)` | 检测试用. `opll.OpllCreateCheckout` exists but the Stripe `payment_pages/{cs}/init` amount read is `stripeInit`, **unexported** |
| **G16** | Session refresh in a browser | 刷新 Session / 刷新K12 — re-visit `/api/auth/session`, then cross-check against `check/v4-2023-04-27`, `accounts/{id}/subscription`, `me` |
| **G17** | Plan classification helpers | `summarize_chatgpt_access_token`, `classify_chatgpt_plan_text`, `merge_chatgpt_backend_plan_summary`, `apply_inferred_plan_to_summary`. `openai.DecodeJWTPayload` exists; the classification does not |
| **G18** | Protocol (browser-less) register path | 协议注册取Session — the whole HTTP-only OAuth+OTP chain and its retry classifier |

### 5.3 Missing subsystems

| ID | Gap | Detail |
|---|---|---|
| **G19** | **`ProviderProxyPoolManager`** | Per-role background pool: target stock 500, low-water 200, ≤30 workers, 60 s take timeout, backoff (1,2,5,10,30), `configure/take/wait_until_ready/stop`, status callback. Plus `ProxyProviderConfig` (`enabled/username/password/endpoint/duration 1..120/regions`), its validators (endpoint = host:port; 8-char sid; `username-region-REGION-sid-XXX-t-N` URL builder) and `PROVIDER_PROXY_ROLES = create/followup/approve`. |
| **G20** | **Payment window opener** | The single most complex missing piece (X1). Persistent context + temp profile, extension load, seeded Chrome Preferences, localStorage seeding (`opencode_paypal_*`, `ppaf_*`), 3× proxy health gate, 1 s page-watch loop, OpenAI confirm auto-click (regex incl. 支払/購入/登録, excluding cancel/back), PayPal `agreements/approve` handling with a random `pp<ts>@gmail.com`, extension UI driving (`#stripe-autofill-btn`, `#saf-input`, `#saf-ok`, `#ppaf-btn`, `#ppaf-phone`, `#ppaf-card`, `#ppaf-fill`), `chrome.storage.local` writes, profile cleanup with 8 retries. |
| **G21** | **Proxy precheck + cleanup orchestration** | The health primitives exist; the concurrent pass, the JP-exit rule, the exit cache keyed by (local, dynamic), and the all-four-pools removal do not. |
| **G22** | **Region filtering / rewriting** | `link_proxy_region_selection_to_code`, `rewrite_proxy_region_code`, `_filter_link_proxies_by_region`, and the **Approve inversion** (`_unlock_approve_proxy_regions` deliberately returns non-target-region proxies **first**, then unknown, then target). Also `_link_proxy_pair/_triple` inheritance (followup←create, approve←followup) and the single-element-pool rule (one proxy may serve all accounts). |
| **G23** | **Session conversion + export** | All 7 formats (`sub2api`, `cpa`, `cockpit`, `9router`, `codex`, `axonhub`, `codexmanager`), `convert_chatgpt_session_record`, `build_session_conversion_document`, `build_sub2api_export`, `account_export_line`, `session_conversion_zip_entry_name` de-duplication, and the CPA pre-refresh gate. |
| **G24** | **Workflow step model** | 7 steps × 6 states, `_set_workflow_step`, and `_workflow_with_derived_state` (which synthesises states from `access_token` / `plus_trial_status` when no record exists). |
| **G25** | **Log classifier/router** | Module prefixing, keyword severity tagging, per-account vs global routing, the Playwright `Call log:` collapse to one line truncated at 360 chars, and the 2000/10000 ring buffers. |
| **G26** | **Account-domain helpers** | `clone_account_for_plus_alias`, `clone_account_for_domain_alias`, alias generation, `_account_uses_cloud_mail`, `_apply_cloud_mail_runtime_config`, `_is_account_email_locked`. |
| **G27** | **Auto-classify** | trial / link / plan modes × all / current / selected scopes. |
| **G28** | **Card head replacement** | `replace_paypal_card_head(baseCard, card)` + the 未用/已用 lifecycle. |
| **G29** | **Turnstile solver probe** | `{url}/health`, `{url}/v1/health`, `{url}/`. (The solver is already passed *into* the auth flow; only the health check is missing.) |
| **G30** | **Audio chime** | Device enumeration + 0.42 s 880/1320 Hz tone. Lowest priority — a WebAudio beep in the frontend is an acceptable substitute. |

### 5.4 Enumerations and constants not in Go

`models` has only `AccountDefaultGroup`, `AccountAllGroup`, `DefaultDomainMailDomain`, `TeamEmailDomain`.
Missing: `ACCOUNT_EMAIL_LOCKED_GROUP` (邮箱锁定), `ACCOUNT_DOMAIN_MAIL_MAIN_GROUP` (域名邮箱主),
`ACCOUNT_DOMAIN_MAIL_CHILD_GROUP` (域名邮箱分), `ACCOUNT_EMAIL_LOCKED_STATUS`,
`PROXY_ROUTE_MODE_OPTIONS`, `LINK_PROXY_REGION_OPTIONS` + the 21 region names,
`ACCOUNT_STATUS_FILTER_OPTIONS`, `ACCOUNT_SORT_*`, `SESSION_CONVERT_FORMATS`,
`ACCOUNT_AUTO_CLASSIFY_MODES`/`_SCOPES`, `WORKFLOW_STEPS`/states, `MAX_PLUS_ALIASES_PER_MAILBOX`,
`PROVIDER_PROXY_ROLES`/`_LABELS`/`_TARGET_STOCK`/`_LOW_WATER`/`_TAKE_TIMEOUT`,
`MAX_LOG_RECORDS_PER_VIEW` (2000) / `MAX_TOTAL_LOG_RECORDS` (10000),
`AUDIO_DEFAULT_DEVICE_LABEL`, `UI_LAYOUT_VERSION`, and the default concurrency/precheck numbers.
Present and reusable: `openai.SMSBowerCountryIDs`, `openai.TurnstileSolverDefaultURL`,
`openai.DefaultPayPalExtensionDir`, `openai.DefaultK12WorkspaceID`,
`openai.AuthProxyFailureRemoveThreshold`, `models.PaymentModes`/`PaymentModeOrder`.

### 5.5 Unexported symbols the UI needs promoted

`opll.extractAccessTokenFromSessionText` (S23/S24 parsing), `opll.stripeInit` (G15),
`mail.extractOpenAICode` (复制验证码), `proxyhealth.normalizeProxyURL` (G5).

### 5.6 Where the maps disagree or are thin

1. **Tab bar vs sidebar.** Map A describes a 7-tab notebook; map H proves the tab layout is deleted.
   **Sidebar-only routing wins.**
2. **Provider proxy roles.** The tree, the labels and the data-model cross-check all say **3 roles**
   (`create`/`followup`/`approve`). One background-work map lists four (`register/create/followup/approve`).
   **Three is correct**; the register pool is a manual textarea, not a provider role.
3. **协议注册取Session money flag.** Map A marks it spends-money; map C read the handler and found
   `phone_provider=None`, `allow_manual_phone=False` with an explicit 未接码/未扣费 message.
   **Map C wins — it does not spend.**
4. **检测试用.** Map A says it "does not open a payment window" and implies harmless; map G found it
   creates a real checkout session and hits `api.stripe.com`. **No charge, but real server-side
   side effects.** Flagged as such above, not marked `$`.
5. **Blocking semantics in map A** are marked "assumed" for ~30 handlers because the bodies were out of
   its range. Every one of those is resolved by maps B/C/D/F/G/H; §2 uses the later maps.
6. **Thin areas — nothing in any map fully specifies these, expect to re-read `app.py`:**
   the `可用操作` context-action strip (S8) — which buttons appear for which selection is built
   dynamically and was never captured; `_refresh_account_sort_headings` arrow rendering;
   the exact `account_export_line` format; the exact per-format shape of all 7 session conversions;
   `MAX_PLUS_ALIASES_PER_MAILBOX`'s value; the 21 region names.
7. **Defects to fix rather than port:** index-as-row-identity (§0.3); the shared stop event cleared by
   the first `open-link-done`; the untimed prompt; a proxy purged from all four pools after only
   2 consecutive transport errors; `清空列表` with no confirmation; `设为 free` silently wiping
   `openai_rt`; the export preview copy/write asymmetry; the overloaded `phone-code-popup` key;
   `headless` ignored by 3 flows. `_click_trial_claim_button`'s scoring block is dead code in Python
   (unreachable after an early return) — Go already gates it behind
   `Config.TrialClaimScoreFallback = false`. Keep it off.

---

## 6. First slice — smallest end-to-end usable app

**Goal: import mailboxes → register → get a Session → generate a payment link → copy it.**
Nothing else. This exercises the whole spine (state, settings, proxy, worker, opll, events,
cancellation) and is the only path that proves the port works.

### Screens (6 of 28)

| Build | Screen | Cut down to |
|---|---|---|
| ✔ | S1 shell + S2 sidebar | **3 nav entries only**: 账户工作台 / 邮箱 / 代理 |
| ✔ | S5 taskbar | Current task + 查看日志 |
| ✔ | S6 toolbar | `导入账号`, `注册取 Session`, `批量提链`, `停止` |
| ✔ | S7 任务参数 | 支付模式, 目标金额, 提链重试, 认证并发, 无头浏览器 |
| ✔ | S8 + S9 account pane | Table (4 columns), 分组 filter, 搜索. **No sorting, no context menu, no group CRUD, no drag-reorder** |
| ✔ | S10 (partial) + S11 + S13 | 结果概览: link Entry + `复制长链接` only (**no workflow tree, no open-link buttons**). Session tab: text + 2 copy buttons. Log tab: both panes |
| ✔ | S14 (partial) | Import textarea + `从文件导入` + `导入账号`. **No Cloud Mail block** |
| ✔ | S17 (partial) | 本地代理, 代理模式, and the **four plain pool textareas**. **No provider tree, no precheck, no cleanup, no region combo, no reuse entries** |
| ✔ | S15 (partial) | SMSBower block only (启用 / API Key / 服务代码 / 国家 ID / 最高单价 / 测试余额 / 保存). Needed because registration hits phone verification |

### Backend work, in dependency order

1. **G1** `Settings` struct + snapshot round-trip (only the ~24 keys these screens touch; the other 36
   must still be preserved verbatim on load/save so the Python app's state file survives).
2. **G5** proxy pool model + `ParseProxyPoolText` + exported `NormalizeProxyURL`; **G6** route-mode gate.
3. **G3** SMSBower `PhoneProvider` implementation. **Non-negotiable** — `worker.Run` cannot finish a
   registration that hits phone verification without it.
4. **G4** task registry + contexts + `stop`.
5. **G2** bindings: `LoadState`, `SaveSettings`, `ImportAccounts`, `ListAccounts`, `SelectAccounts`,
   `StartRegister`, `GenerateLinks`, `StopAll`, `AnswerPrompt`, `CopyText`, `OpenFileDialog`.
6. Event plumbing: `log` (batched), `status`, `account-updated`, `result`, `link-attempt`,
   `task-started`/`task-finished`, `prompt-request`.
7. **G7** batch orchestration — bounded `errgroup`, per-account retry with a fresh proxy,
   `代理耗尽` on exhaustion.
8. **G8** derived status (§1.6) so the table reads correctly.
9. **G25** log classifier — cheap, and account-scoped logs are unusable without it.

### Explicitly out of slice 1

Team (G11/G12), K12 (G13/G14), provider proxy pool (G19), payment window (G20), precheck/cleanup (G21),
region filtering (G22), export/conversion (G23), workflow tree (G24), mailbox manager, auto-classify
(G27), alias/domain generation (G26), trial detection (G15), deactivation scan, protocol register (G18),
audio (G30), the 全部操作 catalogue page, and all six dialogs except the prompt.

### Slice 2 (next, in priority order)

G19 provider pool + G21 precheck + G22 region filtering (these three are what make 批量提链 actually
*work* at volume, not just run) → G20 payment window (the point of the whole tool) → G23 export →
G9/G10 token refresh + account type → the 全部操作 catalogue → Team/K12.

---

## 7. Addendum — region 18580–20150 (the render/event-pump layer)

The original mapping pass lost this region (its agent failed schema validation 5×), so §1–§6 were
written without it. It contains almost no widget construction — it is the **render / refresh / event
pump** layer for widgets built at 12775–13990 — but it owns the account table's derived status and
the event contract, so it materially corrects §1.6 and §4.

### 7.1 Corrections to §1.6 (derived 状态)

Column headings come from `ACCOUNT_SORT_LABELS` (app.py:310): **邮箱 / 类型 / 状态 / 撞链次数** —
§S9 called the fourth column `次数`. `_refresh_account_sort_headings` (19034) appends `↑`/`↓`.

`_account_status_text` (19057) yields: `长链已提取`, `Session已获取`, `成功`, `待处理`,
`K12请求成功`, `K12请求成功/Session已刷新`, `待获取RT(带授权手机号)`.
Refresh variants via `_session_refresh_status_text` (19078): `K12 Session已刷新`, `Plus/Session已刷新`,
`Session已刷新`.

Status filter (`_account_matches_status_filter`, 19136): `全部状态` / `待处理` / `有 Session` /
`Plus` (type in plus,pro) / `Team` / `提链成功` / `失败`.
**`失败` is a substring match over `失败,错误,耗尽,停用,封禁,不可用,拒绝,超时`** — not an enum test.

Search (`_account_visible_indices`, 19108) is multi-term, whitespace-split, AND-ed, over
email+type+status+group.

### 7.2 可用操作 context strip (§S8's "thin area", now specified)

`_refresh_context_actions` (19595) renders **exactly 2 inline buttons + a `更多` overflow menu**:

| Selection | Labels, in order |
|---|---|
| none | `全选可见`, `导入账号`, `全部操作` |
| has session | `刷新 Session`, `检测试用`, `批量提链`, `导出 ZIP` (+`打开长链` if a link exists, +`邮箱管理`) |
| no session | `注册取 Session`, `OAuth 取 RT` (+`打开长链`, +`邮箱管理`) |
| single Team acct | additionally prepends `邀请成员` (primary) and `退出 Team` (danger) |

### 7.3 Event contract (corrects §4)

`_drain_events` (18596) is scheduled at 12462 and drains **≤300 events or 50 ms** per tick, then
reschedules at 10 ms if the queue is non-empty else 100 ms. The 31 event kinds are listed at
18596–19001. Prompt round-trip: `pending_prompts[id]` is a `queue.Queue` the worker blocks on.

### 7.4 Additional port-don't-copy defects (adds to §5.6.7)

These are live bugs found by reading the range; **do not reproduce them**:

1. **Row identity is a list index** — every Treeview iid is `str(index)`, and
   `_apply_account_visible_order` (19734) reorders `self.accounts` **in place**. Any stale iid
   silently addresses a different account. (Already flagged in §0.3; this is the proof.)
2. **The event pump dies on the first unhandled exception.** `_drain_events` catches only
   `queue.Empty` (18998) and the reschedule at 19001 is OUTSIDE the try — one malformed payload and
   the UI stops processing events forever, with no visible error. Wrap per-event.
3. **The `result` event destroys unrelated session fields.** 18641–18675 rebuilds
   `session_results[email]` from a whitelist instead of `{**old, ...}` like every other branch, so
   `workflow`, `access_summary`, `plan_type`, `team_*`, `plus_trial_*`, `email_locked*` are dropped
   every time a link result arrives. **Live data-loss bug.**
4. **Workflow state is inferred by Chinese substring matching on log text** (`_update_workflow_from_status`
   20056, `_update_workflow_from_log` 20088) — emit structured `{step,state,detail}` events instead.
   Two operator-precedence bugs there: 20093 and 20102 both write `A in x or B in x and C in x`,
   where `and` binds only to the last clause.
5. **Unknown trial status renders as green 成功** (20025) — the else-branch of the eligibility
   mapping falls through to SUCCESS.
6. **Rendering mutates the model** — `_render_phones` (19802) assigns `phone.status = "冻结"` as a
   side-effect of drawing.
7. **Destructive bulk mutation from a background event, no confirmation** — `email-locked` →
   `_mark_mailbox_email_locked` (19834) rewrites status+group of every account sharing the alias base
   mailbox and saves. No undo.
8. **`syncing_global_account_list` can stick True** — set on mouse-press (19334), cleared only on
   release (19368); a drag off-window permanently disables side-list sync.
9. **`link_var`/`link_proxy_var` are single globals** written from event handlers for arbitrary
   emails (18687) — a background result overwrites the link box for whatever account is selected.
10. **`save_state()` per event** (18692–18994) — a disk write per event during a burst. Batch it.
11. **Inconsistent email normalization** — `.lower()`, `.casefold()`, `.strip().lower()` and raw keys
    are all used. Pick one canonical key.
12. **O(300·n) per tick** — `_set_account_status`/`_set_account_attempt_count`/`_mark_account_plus`
    each linear-scan all accounts, with `except Exception: pass` around the tree update, so updates to
    filtered-out rows vanish silently.

### 7.5 Dead code in this range — do NOT port

`open_global_account_picker` (19429–19551) and `self.account_picker_window` have **no caller**.
`_refresh_global_account_picker` (19173) returns immediately at a `getattr` guard because
`self.global_account_picker` is never created, which also makes `global_account_picker_var`/`_map`
and `_on_global_account_picker_selected` (19409) dead.
