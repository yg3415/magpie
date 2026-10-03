# Claude Code 原样转发：需求说明

状态：草案，A–I 均已确认（2026-10-03）
范围：仅 magpie 仓库（只放 fork `yg3415/magpie`，不提上游）

## 1. 背景

magpie 现在处理 Claude 订阅请求的方式是在本机启动一个真实的 `claude -p`（bridge，`internal/gateway/claude_subscription.go`），把调用方的请求改写后交给它：

- 调用方的 system 被包进 `<external_system_instructions>`，放进第一条 user 消息；
- 多轮对话被拼成 `Human:` / `Assistant:` 文本；
- 工具改名为 `mcp__magpie__*`，schema 变成 `StructuredOutput` 工具。

所以模型实际收到的输入和直连 Claude Code 不一样，输出可能有不可预期的差异。对于 Claude Code 自己发出的请求，我们希望模型收到的内容和直连完全一致，同时：

1. 在 magpie 的用量页看到流量统计，按账号分开；
2. 继续用 magpie 现有的账号路由策略选择账号。

## 2. 已确认的事实（实测）

| 事实 | 依据 |
|---|---|
| 订阅登录的 Claude Code 设了 `ANTHROPIC_BASE_URL` 后，会把它自己的订阅凭据发给这个地址：`Authorization: Bearer sk-ant-oat…`，`anthropic-beta` 含 `claude-code-20250219,oauth-2025-04-20,…`，路径为 `/v1/messages?beta=true` | 本地抓包 |
| 把这样的请求原样转发到 `api.anthropic.com`，能正常返回 | 本地透明转发脚本，返回 200 |
| 回包头里有完整的额度信息：`anthropic-ratelimit-unified-5h-utilization`、`-7d-utilization`、各窗口 `reset` / `status`、`representative-claim` 等 | 同上 |
| 请求体 `metadata.user_id` 是一个 JSON，含 `device_id`、`account_uuid`、`session_id`；请求头有 `X-Claude-Code-Session-Id`、`User-Agent: claude-cli/2.1.288 (external, sdk-cli)` | 本地抓包 |
| `~/.claude/settings.json` 的 `env` 与启动时的环境变量冲突时，以 settings.json 为准（两边都设 `ANTHROPIC_BASE_URL` 和 `ANTHROPIC_AUTH_TOKEN`，请求发到了 settings.json 指定的地址、带的是它的 token） | 本地测试，Claude Code 2.1.288 |
| 因为请求体带着登录账号的 `account_uuid`，转发时只换凭据去切换账号会前后对不上，所以账号只能在 Claude Code 启动前选定 | 由上一条推出 |

## 3. 目标与非目标

**目标**

- Claude Code 发出的订阅请求，经 magpie 逐字节转发给 Anthropic，模型收到的输入与直连一致。
- 这些请求的用量记入 magpie 用量统计，按账号区分。
- Claude Code 启动时，由 magpie 按现有路由策略选定账号。
- 交互使用和批量 `claude -p` 统一都走这条路。

**非目标**

- 会话中途换账号（账号用完或被限流时，只能报错后重开会话、重跑）。
- 换到其他供应商（订阅登录的 Claude Code 只连 Anthropic）。
- `--resume` / `--continue` 选回原账号。
- 改变现有的账号额度获取逻辑。
- 修改 ai-quota-dashboard。仪表盘在本方案完成后可以退役。
- 修改 Multica 的配置。

## 4. 方案

### 4.1 magpie：Claude Code 原样转发（新的接入模式）

- 转发是 Claude Code 的一种新接入模式（暂名「订阅直通」），和现有的模型接入互斥：Claude Code 要么接入 magpie 用 magpie 的模型，要么用订阅直通，不能同时。不再设单独的全局开关。
- 切到订阅直通时，magpie 改写 `~/.claude/settings.json` 的 `env`（Claude Code 以 settings.json 为准，见第 2 节）：
  - 写入 `ANTHROPIC_BASE_URL` 指向 magpie 网关；这样不经过 wrapper 启动的 Claude Code（IDE 插件、按绝对路径启动等）也走转发，流量都能统计；
  - 去掉 magpie 模型接入写的 `ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_MODEL`、`ANTHROPIC_DEFAULT_*_MODEL`、`ANTHROPIC_SMALL_FAST_MODEL`、`CLAUDE_CODE_SUBAGENT_MODEL`、`CLAUDE_CODE_MODEL_CAPABILITIES` 等，以及 `model`；
  - 用户自己原本的设置照旧，由现有的接入 / 断开逻辑记住并在断开时恢复。
- 断开订阅直通时恢复原样，和现有断开一致。
- gateway 侧不看这个模式，只按请求本身识别：
  - 来自本机（loopback）；
  - `Authorization` 为 `Bearer sk-ant-oat…`；
  - `anthropic-beta` 含 `oauth-2025-04-20`；
  - `User-Agent` 以 `claude-cli/` 开头。
- 分支位置：`handle()`（`internal/gateway/gateway.go` 约 832 行）读完请求体之后、`requestModel` 和 `serve` 之前。`serve` 的第一步就是脱敏改写，之后还会替换模型名，必须在它之前分出去。
- 转发要求：
  - method、路径和 query（含 `?beta=true`）、全部请求头、原始请求体原样发往 `https://api.anthropic.com`；
  - 回包状态码、响应头、响应体（含流式）原样返回；
  - 不复用现有的 `passthrough` / `forwardOnce`：它们会改 UA、`anthropic-version`、beta 头和模型名；
  - 出站代理沿用 Claude 供应商的代理设置（`p.Via` / netproxy）。
- 覆盖路径：除 `/v1/messages` 外，Claude Code 发往 base URL 的其他请求（如 `/v1/messages/count_tokens`）只要满足同样条件，也原样转发；否则会落到 magpie 的 404 或本地估算。
- 旁路读取（不影响转发的字节）：
  - 回包里的 `usage`，用现有 `sniff` 解析器读取，写一条用量记录：provider `claude`，账号按 `account_uuid` 对应到 magpie 保存的登录，model 取请求体，agent `claude`，session 取 `X-Claude-Code-Session-Id`；
  - 429 等失败：按现有逻辑标记账号休息（`restAfter`）；
  - `anthropic-ratelimit-unified-*` 头：交给现有的 `provider.NoteClaudeLimits`，和 bridge 收到 `rate_limit_event` 时写入同一个存储（已确认，问题 A）。
- `maxConcurrency` 对转发请求同样生效：同一账号超过并发上限的请求排队等待（已确认，问题 G）。
- 只读类功能照常工作：最近调用记录、OTel 导出、用量记录、路由记录（trace）。

### 4.2 magpie：选账号接口

- 新增一个本机接口：输入模型和 effort（可空），返回 magpie 为 Claude Code 选定的账号，以及启动 Claude Code 时要用的凭据位置。
- 凭据位置完全按 magpie 现有的保存方式给出，不假定账号数量，也不假定哪个账号在默认槽位（已确认，问题 C）：
  - 选中的是 Claude Code 当前自己登录的账号：凭据就在 Claude Code 默认的位置，返回"默认"；
  - 选中的是其他保存的账号：返回它的配置目录 `~/.config/magpie/claude-accounts/<id>`，目录不存在时按现有逻辑（`claudeSavedDir`）从 `logins.json` 建立。
  - 哪个账号在默认位置由 magpie 实时判断，wrapper 只照用接口的返回。
- 选择逻辑复用现有的 `plan` + `weigh`（`fallback.go`、`routing.go`）：只按 Claude 订阅供应商自己的 `routing` 设置排序（智能 / 按顺序 / 轮流 / 最少使用 / pace），不用分组的设置（已确认，问题 H）；跳过休息中的账号、被 `accountModels` 排除的账号、关闭的账号。不使用会话亲和。
- 每次选择写进路由记录，路由页可见。
- 只接受本机请求。

### 4.3 wrapper：新写，放在本仓库

- 位置：`scripts/claude-wrapper.sh`（暂定），不依赖仪表盘。
- 启动流程：
  1. 从参数里取出 `--model`、`--effort`（没有就为空）；
  2. 请求 magpie 的选账号接口；
  3. 用选中账号的凭据启动真实的 `claude`，并设置 `ANTHROPIC_BASE_URL` 指向 magpie；
  4. 清掉会改变去向的环境变量：`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_API_KEY`，以及 magpie 接入时写的模型名变量。
- 会话、历史、设置尽量仍落在默认的 `~/.claude`，只切换登录凭据：计划用 `CLAUDE_SECURESTORAGE_CONFIG_DIR=<账号配置目录>`。magpie 现在用 `CLAUDE_CONFIG_DIR` 运行保存的账号，钥匙串条目名由这个目录算出（`claudeDirService`）；`CLAUDE_SECURESTORAGE_CONFIG_DIR` 能否命中同一个条目，实现前要实测。命中不了的话只能用 `CLAUDE_CONFIG_DIR`，代价是该账号的会话和历史落在它的配置目录里。
- 启动时 magpie 没在运行、或选账号接口失败：报错退出，不直连（已确认，问题 D）。

## 5. 与现有配置项的共存

原样转发的 Claude Code 请求会跳过所有会改写请求的功能；这些功能对其他请求照常生效。下面按"能否作用于转发请求"分类。

### 5.1 照常生效（只读，不改字节）

最近调用记录（capture）、OTel 导出（含请求体）、用量记录、路由记录（trace）、请求计数、账号休息（遇到 429 时标记）。

### 5.2 对转发请求不生效，但不冲突

这些功能本来就是按请求决定模型或改写内容的，转发请求天然不适用，其他请求不受影响：

- 分组（groups）、分组规则（rules）、分类器 / Jev / `effort: auto`、成员固定 effort、`fast`、`<model>:<level>`；
- 供应商 `fallback`、`modelWires`、`modelAPIs`；
- 图片过滤 / 看图描述（vision）、magpie 代答网页搜索（searcher）、beta 头适配、token 下限重试、thinking 相关改写；
- 会话亲和（affinity）：一个 Claude Code 进程从头到尾只用一个账号。

### 5.3 存在冲突，需要定规则

| 现有功能 | 冲突点 | 建议 |
|---|---|---|
| **脱敏**（`redact`、`redactPersonal`、`redactWords`、`redactRules`，全局） | 脱敏要改写请求体，原样转发做不到 | 已确认：转发请求不脱敏，设置页注明（问题 B） |
| **magpie 接入 Claude Code**（Agents 页给 Claude Code 选模型） | 接入后 Claude Code 发 `magpie` token、用 magpie 的模型名，请求不满足转发条件 | 已确认：订阅直通是一种新的接入模式，和模型接入互斥（问题 E） |
| **自动切换登录**（`keepLogin`、`keepLoginAs`，`provider/codex_switch.go`） | 会改变哪个账号在默认位置 | 已确认：保留。wrapper 启动的由 magpie 每次选账号，不经过 wrapper 启动的靠它换到有余量的账号（问题 F） |
| **请求存档**（`requestArchive`） | 存档本身只读，但会在回包里加一个 `X-Magpie-Archive-Id` 响应头，回包就不再是原样 | 转发请求照常存档，但不加这个响应头 |
| **网关调用方密钥 / 局域网共享**（caller keys、`lan`、key limits） | 转发请求带的是订阅凭据，不是 magpie 密钥，无法识别调用方、套用额度限制；`identifyCaller` 还会把 `Authorization` 改写成 `Bearer magpie`，直接毁掉订阅凭据 | 只转发本机请求；本机请求不带 `sk-magpie-` 密钥时 `identifyCaller` 不会运行。远程机器不在本次范围内 |
| **`maxConcurrency`** | 只排队，不改字节 | 已确认：对转发请求生效（问题 G） |
| **Claude 订阅 bridge** | 不冲突：bridge 继续服务其他客户端（Codex、OpenCode、直接调 API 的脚本）；转发只服务 Claude Code 自己 | 两条路并存 |

## 6. 决定与待定问题

### 已确认

| 问题 | 决定 |
|---|---|
| A. 转发回包的额度信息 | 交给现有的 `provider.NoteClaudeLimits`，不改变其他额度获取逻辑 |
| B. 脱敏开启时 | 转发请求不脱敏 |
| C. 凭据位置 | 不假定账号数量、不假定哪个账号在默认槽位，完全按 magpie 现有的保存位置（见 4.2） |
| D. 启动时 magpie 不可用 | wrapper 报错退出 |
| G. `maxConcurrency` | 对转发请求生效 |
| H. 选账号用哪个路由设置 | 只用 Claude 订阅供应商自己的 `routing` |
| E. 接入与转发 | 转发是 Claude Code 的一种新接入模式，写 settings.json，和模型接入互斥（见 4.1） |
| F. 自动切换登录 | 保留，两者分工见下文 |
| I. 旧 wrapper 的替换 | 要改：whatdiduserssay 的两个批量脚本、全局 PATH（`~/.zshrc` 里仪表盘的管理块）都改为指向新 wrapper |

更正：之前说"magpie 之前按你的要求停用了主动查询 Anthropic 用量接口"是错的。那是上游维护者 yetone 的提交 `9bf6d435`（2026-10-01），提交说明引用的是他那边一位用户的话。

### E. 接入与转发是否互斥（分析记录）

**现状下是互斥的。** magpie 接入 Claude Code 时往 `~/.claude/settings.json` 的 `env` 写入：

- `ANTHROPIC_BASE_URL=http://127.0.0.1:3425`；
- `ANTHROPIC_AUTH_TOKEN=magpie`：Claude Code 有了这个 token 就改用它，不再发订阅凭据，请求不满足转发条件；
- `ANTHROPIC_MODEL`、`ANTHROPIC_DEFAULT_*_MODEL` 等写成 magpie 的模型名（如 `claude/claude-opus-5-5[1m]`）；gateway 发现 settings.json 指向自己时，还会把 Claude Code 原生的 `claude-opus-…` 名字替换成这些模型（tier stand-in）。

settings.json 的 `env` 对所有 Claude Code 会话生效，wrapper 设置的环境变量压不过它（已实测，见第 2 节）。所以接入后，wrapper 启动的 Claude Code 也会走回 magpie 的改写路径，转发静默失效。

**可以做成兼容，但会扩大范围。** 接入的用处是让 Claude Code 能选 magpie 里其他供应商的模型（DeepSeek、Kimi 等）。如果新增一种"保留订阅登录"的接入方式（只写 `ANTHROPIC_BASE_URL`，不写 token、不改模型名），magpie 就可以按模型分流：Anthropic 原生模型走原样转发；选了其他供应商的模型，就按现有逻辑路由（忽略请求里的订阅凭据）。

结论：订阅直通作为一种新的接入方式，只服务 Anthropic 原生模型；要用其他供应商的模型，切回现有的模型接入。

### F. 自动切换登录的作用，以及和转发的关系（分析记录）

**作用：** Claude Code 用 Anthropic 自己的模型时直连 Anthropic，不经过 magpie。它登录的账号用完后，会话会报 "You've hit your session limit"，而 magpie 的网关早已把请求转到别的账号了（#208）。所以 magpie 有一个后台任务（`KeepOnAnAccountWithRoom` → `SwitchWhenSpent`）：

- 定时检查 Claude Code 当前登录的账号，用量超过智能路由认定"用完"的比例时，把 Claude Code 的登录换成下一个有余量的保存账号；
- 原来的账号恢复余量后再换回去；
- 换登录就是把账号凭据在 Claude Code 默认位置和各账号配置目录之间搬动（`switchSavedLogin`）；已经在运行的会话保持原账号，新会话用新账号；
- `keepLogin` 打开时不切换，一直留在用户最先登录的那个账号；`keepLoginAs` 打开时固定留在指定账号。

**和转发的关系：**

- 目的重合：它是为了让"不经过 magpie 的 Claude Code"也能用到有余量的账号。走转发后，wrapper 每次启动都由 magpie 按路由选账号，效果相同而且更细（每个进程一次），对 wrapper 启动的 Claude Code 来说这个后台任务是多余的。
- 仍有用处：不经过 wrapper 启动的 Claude Code（IDE 插件、按绝对路径启动等）仍然只能用默认位置的账号，靠这个任务换到有余量的账号。
- 互相影响：后台任务会改变"哪个账号在默认位置"。选账号接口按 magpie 的实时状态返回位置（见 4.2），所以 wrapper 拿到的位置总是对的。风险在于：一个用默认位置启动的会话正在运行时，后台任务把凭据搬走了。magpie 现有的说明是运行中的会话保持原账号，但 Claude Code 刷新 token 时会重新读写钥匙串，这一点没有验证过。这个风险在今天直连使用时就已经存在，不是转发引入的。

结论：保留，两者并存，接受上面的风险。

## 7. 验收用例

每条用例写明前置条件、操作和预期结果。「自动」表示用 Go 测试或浏览器测试覆盖，「手动」表示用真实账号在本机验证。

### 7.1 接入模式

| 编号 | 前置 / 操作 | 预期 | 方式 |
|---|---|---|---|
| M1 | Claude Code 未接入；在代理页把它切到订阅直通 | settings.json 的 `env` 只多出 `ANTHROPIC_BASE_URL`（指向网关），没有 token 和模型名；用户原有的其他设置不变；代理页显示「订阅直通」，模型选择器和档位只读 | 自动 |
| M2 | Claude Code 处于模型接入；切到订阅直通 | magpie 写入的 token、模型名、`model` 都被去掉，只留 `ANTHROPIC_BASE_URL` | 自动 |
| M3 | 订阅直通；切回模型接入并选一个 magpie 模型 | 恢复模型接入的写法；先弹确认「将退出订阅直通」 | 自动 |
| M4 | 订阅直通；在选择器里选 Claude 原生模型或「默认」 | 先弹确认；确认后退出订阅直通；取消则 settings.json 不变 | 自动 |
| M5 | 订阅直通；「断开 magpie」 | settings.json 恢复到接入前的样子（和现有断开一致）；代理页显示未接入 | 自动 |
| M6 | 订阅直通；手动从 settings.json 删掉 `ANTHROPIC_BASE_URL`，或加回 `ANTHROPIC_AUTH_TOKEN` | 代理页出现漂移提示，说明订阅直通已失效，可以一键恢复 | 自动 |
| M7 | 订阅直通；effort 改为 high | effort 照常生效，仍是订阅直通 | 自动 |
| M8 | CLI：`magpie claude passthrough on` / `off`，`magpie claude`、`magpie ls` | 开关生效；输出显示接入模式；`magpie claude default` 在直通时给出提示 | 自动 |
| M9 | 保存配置档（直通中）→ 切到模型接入 → 应用该配置档 | 回到订阅直通；应用一个没有接入模式字段的旧配置档时只应用字段值 | 自动 |

### 7.2 原样转发

| 编号 | 前置 / 操作 | 预期 | 方式 |
|---|---|---|---|
| P1 | 订阅直通；Claude Code 发一个请求（本地上游替身记录收到的内容） | 上游收到的路径（含 `?beta=true`）、请求头（除 Host、Content-Length 等连接相关的头）、请求体与 Claude Code 发出的逐字节相同 | 自动 |
| P2 | 同上，上游返回流式回包 | Claude Code 收到的状态码、响应头、每个 SSE 事件与上游逐字节相同；没有 magpie 加的头（含 `X-Magpie-Archive-Id`） | 自动 |
| P3 | 非流式回包、4xx、5xx | 原样返回，magpie 不重试、不改写错误、不换供应商 | 自动 |
| P4 | `/v1/messages/count_tokens` 等其他路径，带订阅凭据 | 同样原样转发，不走本地估算、不返回 404 | 自动 |
| P5 | 开启脱敏（含自定义规则），请求里有会被脱敏的内容 | 转发请求不脱敏；其他供应商的请求照常脱敏 | 自动 |
| P6 | 配置了分组、规则、fallback、看图描述、magpie 代答搜索、`modelWires` | 转发请求一律不受影响 | 自动 |
| P7 | 请求来自其他机器（局域网共享开启），带订阅凭据 | 不转发，按现有规则处理（无 magpie 密钥返回 401） | 自动 |
| P8 | 本机请求带 magpie 密钥（`sk-magpie-…`）而非订阅凭据 | 不转发，按现有规则处理 | 自动 |
| P9 | Claude Code 用 magpie token（模型接入）发请求 | 不转发，行为与现在完全一致 | 自动 |
| P10 | 出站代理：Claude 供应商或账号配置了代理 | 转发经该代理发出 | 自动 |
| P11 | 真实环境：同一请求分别直连和经订阅直通（本地抓包对比 Anthropic 收到的内容） | 请求体逐字节相同；回答正常 | 手动 |

### 7.3 用量与额度

| 编号 | 前置 / 操作 | 预期 | 方式 |
|---|---|---|---|
| U1 | 转发一次成功请求 | 用量页多一条记录：Agent 为 Claude Code，供应商 Claude，账号为该请求 `account_uuid` 对应的 magpie 账号，模型、token（含缓存读写）与回包一致，带「直通」徽标 | 自动 |
| U2 | 同一请求在 Claude Code 会话文件里也有记录 | 请求明细只显示一条，不重复计数 | 自动 |
| U3 | 请求的 `account_uuid` 不属于任何 magpie 保存的账号 | 照常转发；用量记录账号显示为邮箱或「未知账号」，不报错 | 自动 |
| U4 | 回包带 `anthropic-ratelimit-unified-*` 头 | 对应账号的 5 小时 / 7 天额度在供应商页、用量页额度、菜单栏更新为回包里的值 | 自动 |
| U5 | 上游返回 429（额度用完 / 限流） | 原样返回给 Claude Code；该账号进入休息，时长按回包的重置时间 | 自动 |
| U6 | 网关页最近调用、路由页 | 转发请求带直通标记和账号；路由过程说明「在 {账号} 上原样转发」；请求体面板注明未脱敏 | 自动 |
| U7 | 同一账号并发请求超过 `maxConcurrency` | 超出的请求排队，依次发出 | 自动 |
| U8 | 开启 OTel 导出 / 请求存档 | 转发请求照常导出、存档；导出和存档内容里看不到订阅凭据 | 自动 |

### 7.4 启动时选账号

| 编号 | 前置 / 操作 | 预期 | 方式 |
|---|---|---|---|
| L1 | 3 个以上已勾选账号，分别试 5 种路由方式 | 选账号接口返回的账号与同条件下 `weigh` 排第一的一致 | 自动 |
| L2 | 某账号在休息中 / 被关闭 / 被 `accountModels` 排除了所请求的模型 | 不会被选中 | 自动 |
| L3 | 选中的是 Claude Code 当前登录的账号 | 接口返回「默认位置」，启动器不设置凭据覆盖 | 自动 |
| L4 | 选中的是其他保存的账号 | 接口返回它的配置目录；目录不存在时按现有逻辑建立；启动器用这个位置启动 Claude Code | 自动 |
| L5 | 每次选择 | 路由页出现一条选账号记录，排序原因用现有路由方式的说明 | 自动 |
| L6 | 接口只接受本机请求 | 其他机器访问被拒绝 | 自动 |
| L7 | 启动器：`claude -p --model sonnet --effort high …` | 选账号时带上模型和 effort；其余参数原样传给 Claude Code；清掉会改变去向的环境变量 | 自动 |
| L8 | 启动器：magpie 没运行 / 接口出错 | 报错退出，提示 magpie 未运行，不直连 | 自动 |
| L9 | 真实环境：用启动器打开 Claude Code，账号 A 被选中 | Claude Code 用 A 的身份发请求（请求里的 `account_uuid` 是 A）；会话、历史仍在 `~/.claude` 下（若实测只能用 `CLAUDE_CONFIG_DIR`，则按 4.3 的退路验收） | 手动 |
| L10 | 真实环境：批量任务连续启动多次 `claude -p`，路由为智能 | 同等条件下一直选同一个账号，system 缓存持续命中 | 手动 |

### 7.5 其他

| 编号 | 前置 / 操作 | 预期 | 方式 |
|---|---|---|---|
| O1 | 自动切换登录照常运行 | 不经过启动器打开的 Claude Code 在默认账号用完后换到有余量的账号；启动器打开的不受影响 | 手动 |
| O2 | Claude Desktop 的 Code 标签页，订阅直通开启 | 它的请求被识别并原样转发、计入用量；识别不了则调整识别条件 | 手动 |
| O3 | 不使用订阅直通 | magpie 行为与现在完全一致（现有测试全部通过） | 自动 |
| O4 | 8.2 的文案 | 中英文按定稿显示，只在订阅直通时出现 | 自动 |
| O5 | 旧启动器替换（问题 I） | whatdiduserssay 两个批量脚本和全局 `claude` 都改用新启动器，批量任务跑通 | 手动 |
| O6 | 构建与测试 | `go vet`、`go test -tags nogui ./...` 通过；相关浏览器测试通过 | 自动 |

## 8. 界面改动清单

现有代码会把订阅直通当成「未接入」：`Wired()`（`internal/agent/applied.go`）只在某个字段是 magpie 模型时才为真，订阅直通只写了 `ANTHROPIC_BASE_URL`，所以「断开 magpie」不出现、`Disconnect()` 什么也不做，配置被改动后也不会提示。另外，在模型选择器里选 Claude 原生模型或「默认」，会删掉 `ANTHROPIC_BASE_URL`（`claude.go` 的 `unroute()` / `set("")`），选 magpie 模型会写回 token 和模型名，三种操作都会静默结束订阅直通。所以后端要先给 Claude Code 增加「接入模式」的概念，界面才能跟上。

### 8.1 必改

| 位置 | 现状 | 改动 |
|---|---|---|
| **代理页 · Claude Code 行**（`renderAgents` / `agentRow`，app.js 232–409；API `agentJSON`、`/api/set`、`/api/agents/{action}/{id}`） | 只有模型、effort、档位等字段；接入状态由 `wired` 判断 | 新增接入模式选择（模型接入 / 订阅直通 / 不接入）；订阅直通时模型选择器和档位改为只读或隐藏，显示「订阅直通」标记；effort 保留（它不属于模型接入） |
| **代理页 · 模型选择器**（`openPicker` 2060，`foldSame` 2038，「默认」2079，「断开 magpie」2083） | 选原生模型、默认或 magpie 模型都会改写 settings.json | 订阅直通时，这三种操作要先确认「将退出订阅直通」 |
| **代理页 · 行菜单**（`openAgentMenu` 1378–1399） | 「重新应用」「保留当前设置」「断开 magpie」只在 `wired` 时出现 | 增加切换到订阅直通 / 切回模型接入；「断开 magpie」覆盖订阅直通 |
| **代理页 · 断开确认框**（`askDisconnect` 726） | 文案是「端点、密钥、模型和 effort」 | 增加订阅直通版本的文案 |
| **代理页 · 配置漂移提示**（`driftFix` 698，`DRIFT_WHY` 710–714；后端 `Drift()`） | 只检测 magpie 模型被改掉 | 增加订阅直通的漂移：base URL 被删、token 或模型名被加回导致直通失效 |
| **代理页 · 「直连 Anthropic，不经过 magpie」说明**（`directSaid` 203，`commit()` 2653） | 原生模型一律标为不经过 magpie | 订阅直通时改为「经 magpie 原样转发」 |
| **代理页 · 启动命令**（可复用 agy 的 `launchButton` 1508） | Claude Code 没有 | 订阅直通时提供 wrapper 的启动命令，并提示已运行的会话要重启才生效 |
| **设置 · 隐私 / 脱敏**（`renderRedact` 13094） | 脱敏对所有请求生效 | 注明 Claude Code 订阅直通的请求不脱敏 |
| **用量页 · 请求明细**（`renderLedger` 10418，徽标 10547–10553，详情 `ledDetail` 9941；API `ledgerPage`） | 订阅账号显示「订阅账号」徽标 | 增加「直通」徽标；详情里显示模式。需要在用量记录里加一个字段标记直通 |
| **用量页 · 去重**（`internal/usage/request_page.go`，`visibleLocal`、`matchKey`） | 会话日志的记录和网关记录按规则合并 | 直通请求两边都有记录，必须合并成一条，否则重复计数 |
| **网关页 · 最近调用**（`renderActivity` 4167；`gateway.Call`） | 显示 agent、模型、协议，没有账号 | 直通请求加标记和账号；请求体面板注明「未脱敏」 |
| **路由页**（`routing.js`：`reqRow`、`renderSteps`、`KIND`；API `/api/gateway/trace`） | 每个请求一段路由过程（候选、分组、亲和） | 直通请求加类型标记，过程写「在 {账号} 上原样转发，账号在 Claude Code 启动时选定」；选账号接口的每次选择也作为一条路由记录显示，用现有的路由方式说明文案解释排序 |
| **CLI**（`main.go`：`magpie claude`、`magpie ls`、`magpie claude default`） | 只打印字段值；`default` 会结束直通；没有断开命令 | 显示接入模式；新增类似 `magpie claude passthrough on|off` 的命令和帮助文本；`default` 在直通时给出提示 |

### 8.2 改文案或补说明（定稿）

界面文案以英文为键，中文写进 `internal/gui/assets/i18n.js`。下面的文案只在 Claude Code 处于订阅直通时替换或追加，其他情况保持原文。

**统一用语**

| 概念 | 英文 | 中文 |
|---|---|---|
| 新的接入模式 | Subscription passthrough | 订阅直通 |
| 新 wrapper | magpie's launcher | magpie 启动器 |

**1. 供应商页 · Claude 编辑器 ·「账号」字段说明**（app.js 5234，替换原说明）

- en: Tick every account to use. Claude Code started through magpie's launcher gets one of them each time it starts, as Routing says, and keeps it until it exits. Claude Code started any other way uses the account it is signed in to.
- zh: 勾选要使用的账号。通过 magpie 启动器打开的 Claude Code，每次启动时按「路由」选其中一个，一直用到退出。用其他方式打开的 Claude Code 使用它当前登录的账号。

**2. 同上 ·「路由」说明的后半句**（`renderRouting` 的 `own()`，app.js 6669–6679，四种情况各替换一句）

| 情况 | en | zh |
|---|---|---|
| 保持登录在指定账号 | Routing picks the account each time Claude Code starts through magpie's launcher, in the accounts' order; Claude Code started any other way stays signed in to {user}, whatever it has left. | 通过 magpie 启动器打开的 Claude Code，每次启动时按账号顺序选账号；用其他方式打开的 Claude Code 始终登录 {user}，不论它还剩多少额度。 |
| 保持登录在第一个账号 | Routing picks the account each time Claude Code starts through magpie's launcher; Claude Code started any other way stays signed in to the first account, whatever it has left. | 通过 magpie 启动器打开的 Claude Code，每次启动时按路由选账号；用其他方式打开的 Claude Code 始终登录第一个账号，不论它还剩多少额度。 |
| 按顺序 | Routing picks the account each time Claude Code starts through magpie's launcher; Claude Code started any other way uses the one it is signed in to, which magpie moves to the next ticked account with room once it is used up, and back to the first once that has room again. | 通过 magpie 启动器打开的 Claude Code，每次启动时按路由选账号；用其他方式打开的 Claude Code 使用当前登录的账号，这个账号用完后，magpie 会把它换到下一个有余量的已勾选账号，第一个账号恢复余量后再换回来。 |
| 其他路由方式 | Routing picks the account each time Claude Code starts through magpie's launcher; Claude Code started any other way uses the one it is signed in to, which magpie moves to the next ticked account with room once it is 98% used, and back to the first once that has room again. | 通过 magpie 启动器打开的 Claude Code，每次启动时按路由选账号；用其他方式打开的 Claude Code 使用当前登录的账号，这个账号用到 98% 后，magpie 会把它换到下一个有余量的已勾选账号，第一个账号恢复余量后再换回来。 |

**3. 同上 ·「保持登录」说明**（app.js 7560，替换原说明）

- en: magpie won't sign Claude Code in to another account when the first runs low. This only matters for Claude Code started any other way than through magpie's launcher.
- zh: 第一个账号额度不足时，magpie 不会把 Claude Code 换到其他账号登录。这只影响不通过 magpie 启动器打开的 Claude Code。

**4. 同上 ·「备用」说明**（`fallbackHint`，app.js 6770，在原说明后追加）

- en: Claude Code's own requests don't move to these: with subscription passthrough they always go to Anthropic.
- zh: Claude Code 自己的请求不会改走这些模型：订阅直通下它们始终发给 Anthropic。

**5. 供应商页 · Claude 行的代理图标**（app.js 2940）

订阅直通的 Claude Code 也列出，鼠标悬停提示：
- en: Claude Code · Subscription passthrough
- zh: Claude Code · 订阅直通

**6. 添加 Claude 账号时的风险提示**（`SUBS[0].riskNote`，app.js 6791，替换原文，不区分模式）

- en: Anthropic may suspend or ban a Claude account it sees used outside its own apps. Other agents reach it through Claude Code, and Claude Code with subscription passthrough sends its requests as it always does, but Anthropic may still act on them; you use it at your own risk. Use an account you can afford to lose.
- zh: Anthropic 发现 Claude 账号在它自己的应用之外使用时，可能会暂停或封禁该账号。其他 Agent 通过 Claude Code 使用它；订阅直通下的 Claude Code 照常发送自己的请求。但 Anthropic 仍可能采取措施，风险由你自行承担。请使用一个即使丢失也能接受的账号。

**7. 代理页 · 菜单栏面板里的 Claude Code 行**（app.js 315–381）

订阅直通时，模型位置显示模式名「Subscription passthrough / 订阅直通」，不显示原生模型名。

**8. 菜单栏原生菜单 · 账号卡片标题**（`trayInUseCard`，trayusage.go）

订阅直通时，标题从「Account in use / 正在使用的账号」改用已有的「Signed in / 已登录」，内容不变。

**不加文案的地方**（用户不需要知道）

- 会话保持：订阅直通下一个 Claude Code 从启动到退出本来就只用一个账号，没有可说明的差别。
- 并发上限：对订阅直通照常生效，行为与说明一致，不必另写。
- 网关页状态的「{n} 个 Agent 经由此路由」：订阅直通的 Claude Code 计入即可，文案不变。
- 网关页「接入」：启动器命令放在代理页 Claude Code 行，这里不重复。
- 可观测性、请求存档：照常工作；去掉订阅凭据、不加响应头属于实现细节。
- 路由组、多账号页：订阅直通的 Claude Code 用的是 Claude 原生模型，不会选到路由组；多账号页的「路由」与 Claude 编辑器是同一个设置，说明已在编辑器里。
- TUI、`magpie accounts` 图例：只需显示接入模式（见 8.1），现有文案不变。

### 8.3 其他受影响的点（已决定）

- **配置档（profiles）**：配置档记录 Claude Code 的接入模式。保存时一并保存；应用一个保存了订阅直通的配置档时切到订阅直通，应用保存了模型的配置档时切回模型接入。没有这个字段的旧配置档按现在的方式处理（只应用字段值）。理由：配置档的用途是「快照每个代理的设置」，不记录模式的话，应用一个旧配置档会静默退出订阅直通。
- **Claude Desktop 的 Code 标签页**：接受它一起走订阅直通。它运行的就是 Claude Code、读同一个 settings.json、用自己的订阅登录，走转发正好符合「统一都走」，它的流量也能统计。代价是 magpie 没运行时它也连不上。实现时要实测它发出的请求是否满足识别条件（尤其是 `User-Agent` 是否以 `claude-cli/` 开头），不满足就调整识别条件。
- **菜单栏的「使用中的账号」**：保持现有含义，显示 Claude Code 当前登录的账号（不经过 wrapper 启动时用的那个）。订阅直通时把标签改为「已登录账号」，避免误以为所有会话都在用它；不新增其他信息。
- **会话页**：直通请求在会话详情里以 `claude/<模型>` 出现，和 bridge 的显示一致，不改。

### 8.4 文案与测试

- 界面文案只有一张中文表：`internal/gui/assets/i18n.js`（`I18N.zh`，以英文原文为键）。Go 侧返回的提示和漂移说明也要在这里加对应条目才会显示中文。TUI 和 CLI 不翻译。
- 需要更新的浏览器测试（`internal/gui/tests/`）：`agent-disconnect`、`claude-direct`、`claude-picker-fold`、`model-pick`、`keep-login`、`claude-risk`、`usage-ledger`、`usage-ledger-detail`、`routing-kind`、`redact-rule-row`、`otel`、`request-archive`、`tray-inuse` 等。
- 需要新增：`agent-passthrough`（模式切换、选择器只读、断开、漂移），`routing-passthrough` / 用量直通徽标。
- Go 测试：`internal/agent/claude_test.go`、`disconnect_test.go`、`applied_test.go`，`internal/gui/agent_disconnect_test.go`、`usage_test.go`、`trace_test.go` 等。
