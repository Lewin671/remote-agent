# Antigravity CLI 实测记录

本文记录 remote-agent 接入 Antigravity CLI（`agy`）所依据的实测结果。写法与 [claude-code-notes.md](claude-code-notes.md) 和 [codex-notes.md](codex-notes.md) 一致：只写 Antigravity CLI 实际怎么表现；remote-agent 据此的设计见 [architecture.md](architecture.md) 第 19 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果，推断或没测过的会在句中标「推测」「未验证」。升级 Antigravity CLI 后，涉及的条目要重新验证。所有事实均有对应的原始实测证据记录文件保存在证据目录 `evidence-A/` 中。

最近一次验证：Antigravity CLI **1.4.12**，macOS（arm64），tmux 3.7c，2026-11-03。实测在独立的 tmux socket（`-L ra-agy-notes`）和隔离的临时工作区（`/tmp/ra-agy-*`）里进行。本机 `~/.local/shims/agy` 设置代理指向 Clash（`127.0.0.1:7897`），下文凡是受其影响的地方会注明。

标注了版本的条目以该版本为准；升级后重新验证过的会在版本号里带上新版本。

## 1. 启动、工作区与会话元数据

| 参数 | 作用 |
|---|---|
| `--settings <JSON 或文件>` | 覆盖/叠加一份设置，只对当前会话生效，不持久化到全局 `settings.json`。若传入未知字段会报错退出（如传入 `"hooks"` 会报 `unknown setting "hooks"`）。可用于传递 `trustedWorkspaces`、`statusLine`、`toolPermission` 等合法配置 |
| `--add-dir <目录>` | 挂载额外的扩展工作区目录（可多次指定）。**实测关键用途**：可在该目录下放置 `.agents/hooks.json` 与 `.agents/mcp_config.json`，实现对单个会话无侵入注入 Hook 和 MCP。**实测信任表现**：被挂载的目录**不需要**加入 `trustedWorkspaces`，agy 仅在启动时对主工作区进行信任检查，`--add-dir` 目录下的 Hook 和 MCP 会被直接加载执行 |
| `--mode <模式>` | 设定当前会话执行模式，支持 `accept-edits`、`auto`、`plan`。传入未知模式会警告并使用默认模式（`warning: unrecognized --mode value ...`） |
| `--dangerously-skip-permissions` | 启动标志，自动批准所有工具执行权限，跳过原生权限审批弹窗。一旦以该参数启动，会话内无法退回询问模式 |
| `--conversation <UUID>` | 恢复指定会话（第 1 节「会话恢复」）。若 UUID 在本地不存在，输出 `warning: conversation "<id>" not found` 并回退启动全新的会话 |
| `-c` / `--continue` | 继续最近一次会话 |
| `-p` / `--prompt <文本>` | 非交互模式：执行单轮 prompt 后输出结果并退出（注：headless 模式下若遇到需要权限的操作会直接拒绝，无法弹窗） |
| `--model <模型 ID>` | 启动模型（如 `gemini-3.8-flash-low`、`gemini-4-argon-medium`，详见第 6 节） |
| `--effort <档位>` | 思考档位（`low`、`medium`、`high`，详见第 6 节） |
| `--log-file <路径>` | 覆盖默认的 CLI 日志输出路径 |
| `--version` | 输出 `Antigravity CLI 1.4.12` |

> [!NOTE]
> 实测确认：命令行不存在 `--tool-permission-mode` 参数（若在交互启动中传入该参数，CLI 报错 `flags provided but not defined: -tool-permission-mode` 并退出）。权限控制通过配置项 `toolPermission`（可经 `--settings` 传入）或运行时标志 `--dangerously-skip-permissions` 实现。

- **CLI 命令名称与路径**：CLI 可执行文件名为 `agy`，真实二进制路径在 `~/.local/bin/agy`（Mach-O 64-bit arm64）。系统中没有名为 `antigravity` 的独立命令（实测 `which antigravity` 返回未找到）。本机 `~/.local/shims/agy` 是一个注入了 `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` 代理环境变量的 shell shim，daemon 启动时需自行注入代理变量（第 10 节）。
- **工作区信任（Workspace Trust）**：
  - 在从未信任过的目录中以交互模式启动 `agy` 时，终端会弹出全屏信任询问：
    ```text
    Accessing workspace:
      /tmp/ra-agy-work
    Quick safety check: Is this a project you created or one you trust?
    > Yes, I trust this folder
      No, exit
    ```
  - **交互特征**：光标**默认停留在 `> Yes, I trust this folder`**。若用户直接按回车即确认信任；按数字键 `1` 也立即确认信任；选 `No, exit` 则直接退出进程。
  - 信任记录写入 `~/.gemini/antigravity-cli/settings.json` 的 `"trustedWorkspaces": [...]` 数组中。
  - **绕过弹窗的最佳实践**：在启动时直接传递命令行参数 `--settings '{"trustedWorkspaces": ["<当前工作区真实路径>"]}'`，即可**彻底跳过信任弹窗**，直接进入正常就绪状态，且**完全不修改**用户的全局 `settings.json`。
- **会话 ID（Conversation ID）生成与提取**：
  - 会话 ID 为标准 UUIDv4 格式（36 字符，如 `aaf0e122-e1bf-40be-af19-dacc02201653`）。
  - **无法在启动时外部预设新 ID**：传入新生成的 UUID 给 `--conversation` 会触发 warning 并回退到内部新生成 UUID。
  - **新会话 ID 的提取时机**：
    1. **启动后立即可查**：`agy` 启动后立刻在 `~/.gemini/antigravity-cli/presence/<UUID>.lock` 创建文件锁。通过比对启动前后新增的 lock 文件，可在首个 prompt 提交前准确拿到会话 ID。
    2. **Hook 提取**：首个 prompt 提交时触发 `SessionStart` hook，payload 中的 `conversationId` 字段即为该会话 ID。
    3. **历史记录提取**：第二轮 prompt 开始，`history.jsonl` 中记录包含 `conversationId`。
- **会话恢复（Resume）**：
  - 执行 `agy --conversation <已有会话 UUID>`。
  - 会话继续在原 `brain/<id>/` 目录下追加写入 `transcript.jsonl`，`step_index` 从上一次退出的序号连续递增。
  - 恢复后模型收到系统提示 `[Notice] All your subagents and background tasks have been stopped due to server restart`。
- **会话分支（Fork）**：
  - TUI 原生支持 `/fork` 命令。
  - 执行 `/fork` 后，`agy` 在 `~/.gemini/antigravity-cli/conversations/<new-id>.db` 完整克隆原会话的 SQLite 数据库，并切换到新的 `presence/<new-id>.lock`。
  - **日志隔离**：分支后的新会话独立写入自己的 `brain/<new-id>/.system_generated/logs/transcript.jsonl`，步骤编号从 N+1 起连续递增，**不会拷贝父会话历史 transcript 行**。
  - 屏幕输出反馈：
    ```text
    > /fork
      ⎿  Forked conversation.
         To go back, use: /resume <原会话 UUID>
    ```

## 2. 本地状态与数据存储

Antigravity CLI 的所有运行时状态和会话数据均存放在 `~/.gemini/antigravity-cli/` 下：

- **目录结构**：
  - `brain/<conversation-id>/`：会话主目录。
    - `.system_generated/logs/transcript.jsonl`：紧凑型会话日志（部分大字段截断）。
    - `.system_generated/logs/transcript_full.jsonl`：全量会话日志（无截断）。
    - `.system_generated/logs/media/`：会话中引用的二进制媒体文件（如截屏图片 `step_X_media_0.png`）。
    - `.system_generated/tasks/`：后台异步任务日志。
    - `scratch/`：临时脚本运行空间。
    - `<工件名称>.md`：模型生成的方案、计划等工件文件。
  - `conversations/<conversation-id>.db`（及 `-wal`、`-shm`）：单个会话的 SQLite 状态库，内部存储 protobuf 编码的序列化消息树。
  - `conversation_summaries.db`：全局会话摘要 SQLite 数据库，用于索引历史会话。
  - `history.jsonl`：全局命令与输入历史。
  - `presence/<conversation-id>.lock`：活跃会话文件锁。
  - `log/cli-YYYYMMDD_HHMMSS.log`：CLI 调试日志，软链接 `cli.log` 始终指向最新日志。
  - `settings.json`：全局持久化用户配置。

- **实时追加写入验证（Live Append）**：
  - 实测在执行长耗时工具（如 `sleep 20`）时以 0.5s 周期轮询 `transcript.jsonl`：
    - 在命令执行期间（t=2.98s），模型的 `PLANNER_RESPONSE`（包含 `tool_calls: [run_command]`）**已经落盘写入文件**，而非等待命令结束后一次性写入。
    - 历史已写入行在整个会话生命周期中**完全不可变（Immutable）**，字节数和哈希值严格保持不变。
    - 外部读取端可安全地按增量字节流或按行追加读取，无需担心回溯改写。

- **双日志机制（transcript vs transcript_full）**：
  - `transcript.jsonl` 与 `transcript_full.jsonl` **行号与步骤严格 1:1 对应**。
  - 每行均为合法 JSON 对象，具备以下标准字段：`step_index`、`source`（`USER_EXPLICIT` / `MODEL` / `SYSTEM`）、`type`、`status`（`DONE` / `ERROR` / `RUNNING`）、`created_at`。
  - **截断规则**：当模型思考（`thinking`）或输出（`content`）超长时，`transcript.jsonl` 会截断该字段，并在行尾添加 `truncated_fields: ["content"]`；而 `transcript_full.jsonl` 中始终保留完整字段。
  - **读取特征**：外部程序监听时可首选 `transcript.jsonl`；仅在遇到 `truncated_fields` 时，根据行号定向读取 `transcript_full.jsonl` 对应行。

- **输入历史记录（history.jsonl）真实规格**：
  - 路径：`~/.gemini/antigravity-cli/history.jsonl`。
  - **标准键结构**：包含 `display`（输入正文）、`timestamp`（毫秒时间戳）、`workspace`（工作区绝对路径）、`conversationId`（会话 UUID，特定情况下缺省）、`type`（类型，如 `"slash_command"`）。
  - **`conversationId` 缺省场景**：
    1. 会话的**首个 prompt**：由于在回车提交瞬间写入 history.jsonl，此时后端尚未下发绑定 conversationId，因此该记录无 `conversationId` 键；
    2. 在任何 prompt 提交前运行的 Slash 命令（如 `/exit`、`/model`、`/help`）：无 `conversationId` 键。
    3. 首轮回复完成后，第二轮起的所有 prompt、排队消息及 Slash 命令均**严格附带 `conversationId`**。
  - **多行粘贴（Multi-line Paste）表现**：
    - 在终端中粘贴多行文本时，viewport 视口为保持界面整洁会将其折叠渲染为 `[Pasted text #1 +23 lines]`。
    - 但在 `history.jsonl` 的 `display` 字段中，**始终保存完整文本**（包含 `\n` 换行符），绝不会截断为占位符。
  - **排队消息（Queued Messages）**：
    - 在会话忙碌期间键入并回车的消息，会在敲击回车瞬间立即落盘写入 `history.jsonl`，且**附带当前的 `conversationId`**。

- **用户消息与元数据包装格式**：
  - 用户提交的消息在 transcript 的 `USER_INPUT` 记录中被结构化包装：
    ```text
    <USER_REQUEST>
    用户输入正文（多行原样保留）
    </USER_REQUEST>
    <ADDITIONAL_METADATA>
    The following is contextual metadata:
    Time: 2026-11-03 09:59:58 PST
    </ADDITIONAL_METADATA>
    ```

- **多媒体与图片存储（Images）**：
  - 当模型通过 `read_file` 读取图片（如 PNG、JPEG）或用户上传图片时，二进制数据**不以内联 Base64** 存放在 transcript 中。
  - 系统将图片存入 `brain/<id>/.system_generated/logs/media/step_<X>_media_0.png`。
  - transcript 中的 `GENERIC` 步骤通过 `media` 数组引用本地文件：
    ```json
    "media": [{"mime_type": "image/png", "uri": "file:///Users/.../media/step_12_media_0.png"}]
    ```

## 3. 拦截机制（Hook 与 MCP）

### 3.1 Hook 系统

- **配置方式与生效范围**：
  - `agy` 的 `--settings` 参数**不支持**直接注入 `hooks`（传入 `{"hooks": ...}` 会报错 `Error: --settings: unknown setting "hooks"`）。
  - **会话级无侵入挂载方案**：
    创建临时目录（如 `/tmp/ra-addon`），在其下创建 `.agents/hooks.json`，启动时传入 `--add-dir /tmp/ra-addon`。
  - **工作区信任与 Add-Dir 实测表现**：
    实测证实，通过 `--add-dir <dir>` 挂载的目录**不需要**预先加入 `trustedWorkspaces`。agy 仅在启动时对当前主工作区检查信任状态；只要主工作区受信任，`--add-dir` 目录内的 `.agents/hooks.json` 与 `.agents/mcp_config.json` 会被正常加载并执行。
- **Hook 规范（官方内置文档与实测核对）**：
  - 顶级键为 Hook 命名标识（如 `"test-gate"`），工具类 Hook（`PreToolUse`、`PostToolUse`）必须按 matcher 与 hooks 数组包裹：
    ```json
    {
      "test-gate": {
        "PreToolUse": [
          {
            "matcher": ".*",
            "hooks": [
              {
                "type": "command",
                "command": "./scripts/hook.sh",
                "timeout": 30
              }
            ]
          }
        ],
        "PostToolUse": [
          {
            "matcher": ".*",
            "hooks": [
              {
                "type": "command",
                "command": "./scripts/post.sh"
              }
            ]
          }
        ],
        "SessionStart": [{"command": "./scripts/start.sh"}],
        "PreInvocation": [{"command": "./scripts/pre-inv.sh"}],
        "PostInvocation": [{"command": "./scripts/post-inv.sh"}],
        "Stop": [{"command": "./scripts/stop.sh"}]
      }
    }
    ```
- **支持的生命周期事件（Lifecycle Events）**：
  官方内置文档与实测确认支持且仅支持以下 6 个生命周期事件：
  1. `SessionStart`：新会话启动或恢复、进入首轮交互时触发一次。
  2. `PreToolUse`：每次执行工具步骤前触发（支持 `matcher` 过滤工具名）。
  3. `PostToolUse`：工具执行完成（无论成功或失败）后触发。
  4. `PreInvocation`：调用模型之前触发（可注入 `injectSteps` 或设置 `terminationBehavior: "terminate"`）。
  5. `PostInvocation`：模型推理与工具调用完成后触发（支持 `terminationBehavior: "force_continue"` 或 `"terminate"`）。
  6. `Stop`：交互执行循环终止时触发（payload 附带 `terminationReason`，枚举值包含 `NO_TOOL_CALL`、`MAX_INVOCATIONS`、`MAX_FORCED_INVOCATIONS`、`MAX_TOKEN_BUDGET_EXCEEDED`、`ERROR`、`USER_CANCELED`、`TERMINAL_STEP_TYPE`、`TERMINAL_CUSTOM_HOOK`、`INJECTED_RESPONSE`）。
  > [!IMPORTANT]
  > **注意**：agy **不支持** `UserPromptSubmit` 与 `SessionEnd` 事件（内置文档与实测均无此事件，请勿套用其他 CLI 的事件体系）。

- **Hook Handler 字段与工作目录**：
  - `type`（可选）：默认为 `"command"`，当前仅支持命令执行。
  - `command`（必填）：通过 shell 运行的脚本命令（Unix 下为 `sh -c`）。**工作目录（cwd）自动设置为包含 `hooks.json` 的目录**。
  - `timeout`（可选，整数秒）：执行超时阈值，**官方默认值为 30 秒**。
    - **超时表现实测**：配置 `"timeout": 2` 并执行 `sleep 10`，满 2 秒后 Hook 进程被 CLI 以 `SIGKILL` 杀死；终端显示 `The command timed out in the hook`；`transcript.jsonl` 中记录 `GENERIC` 类型错误步骤：`status: "ERROR"`, `error: "JSON hook \"...\" failed: command failed: signal: killed, stderr: "`。
    - **调整上限实测**：配置 `"timeout": 3600`，CLI 正常解析且无报错，可用于支持长耗时的挂起审计。

### 3.2 PreToolUse 拦截与原生权限提示时序

- **严格时序（Ordering 验证）**：
  - 实测高精度时间戳记录：
    - Hook 启动：`t=12.889s`
    - Hook 阻塞等待 2 秒并退出：`t=14.947s`
    - 终端屏幕上首次渲染出 `Run this command?` 权限弹窗：`t=15.017s`
  - **结论**：`PreToolUse` Hook **严格在原生确认弹窗出现之前**执行。Hook 运行或阻塞期间，终端保持在忙碌状态（Braille spinner 旋转），原生弹窗绝不会提前弹出，用户无法在 Hook 做出决策前抢答。
- **PreToolUse 不等于会弹窗（PreToolUse ≠ will prompt）**：
  - 在 `toolPermission: "request-review"` 模式下，模型执行 `read_file` 时：
    - `PreToolUse` Hook **依然正常触发并执行**（记录工具名 `tool: "read_file"`）；
    - 但 Hook 执行完毕放行后，终端**完全不弹窗**，工具直接执行成功。
  - 这证实 `PreToolUse` 是对**所有工具调用**的通用前置生命周期，并不意味着该调用一定会向用户索权。
- **Hook 输入/输出契约与实测行为**：
  - **标准输入（stdin）**：接收 JSON 元数据，包含 `toolCall: {name, args}`、`stepIdx`、`conversationId`、`workspacePaths`、`transcriptPath` 等。
  - **标准输出（stdout）字段**：
    - `decision`（字符串，必填）：支持 `"allow"`、`"deny"`、`"ask"`、`"force_ask"`。
    - `reason`（字符串，可选）：向用户或模型解释的原因文案。
    - `permissionOverrides`（字符串数组，可选）：临时权限授权（如 `["command(echo X)"]`）。
    - `overwrite`（对象，可选）：对工具参数进行浅层（shallow top-level）重写。
  - **各决策值的实测表现**：
    1. **`{"decision": "approve"}`（非法决策）**：
       - 实测结果：工具调用直接报错失败！transcript 记录 `status: "ERROR"`, `error: "unsupported hook decision: approve"`。
       - 终端显示：`The command execution failed due to an environment/hook error: unsupported hook decision: approve.`，不会弹出任何审批窗口。
       - 结论：**`approve` 不是合法的 decision 取值，绝不会跳过审批**。
    2. **`{"decision": "allow"}` 与 `permissionOverrides`（在 `request-review` 下）**：
       - 实测结果：**原生审批弹窗依然出现**！
       - 测试返回 `{"decision": "allow"}` 或 `{"decision": "allow", "permissionOverrides": ["command(...)"]}` 时，终端仍然如常渲染 `Requesting permission for: ... Run this command? 1. Yes, run command ...`。
       - 结论：在 `request-review` 模式下，Hook 输出 `allow` 仅代表 Hook 本身不阻断，**无法豁免或压制原生的命令行确认弹窗**。
    3. **`{"decision": "deny", "reason": "..."}`**：
       - 实测结果：立即阻断工具调用。终端完全不出现原生弹窗，transcript 记录状态为 `ERROR` 的工具结果（`content: "tool call denied by pre-tool hook: ..."`），模型继续回复。
    4. **`{}`（空 JSON 输出）**：
       - 实测结果：**直接被视为拒绝（Denial）**！transcript 记录 `error: "tool call denied by pre-tool hook: "`，工具调用直接失败。
       - 原因：内置契约中 `decision` 为必填字段；空对象解析出的 `decision` 为空字符串，走入拒绝分支且带有空字符串原因。
    5. **`{"decision": "ask", "reason": "..."}`**：
       - 实测结果：在原生弹窗顶部附带显示 `Reason: <文案>`，并提示用户确认。
    6. **退出码 0 且无输出（stdout 为空）**：
       - 放行至底层策略审核；若底层模式需要审批，则继续呈现原生权限提示。

### 3.3 原生权限审批弹窗（Native Permission Prompt）

- **触发条件**：
  在 `toolPermission: "request-review"` 或 `"strict"` 模式下，当模型尝试执行需要审核的操作时触发。
- **命令行权限弹窗（RunCommand）**：
  ```text
  Requesting permission for:
     rm -f /tmp/dummy.txt

  Run this command?
  > 1. Yes, run command
    2. Yes, and always allow in this conversation for commands that start with 'rm -f /tmp/dummy.txt'
    3. Yes, and always allow for commands that start with 'rm -f /tmp/dummy.txt' (Persist to settings.json)
    4. No, cancel

    ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
  ```
- **文件写入权限弹窗（WriteFile / EditFile）**：
  ```text
  /private/tmp/ra-agy-a2-work/test_out.txt  +1
    1  +  123

  Allow creation of this file?
  > 1. Yes, allow creation
    2. No, deny creation

    ↑/↓ Navigate · tab Amend · f full diff
  ```
- **文件读取权限弹窗（ReadFile，仅在 `strict` 模式出现）**：
  ```text
  File access
  Read: /private/tmp/ra-agy-a2-work/sample.txt

  Allow access to this file?
  > 1. Yes, allow access
    2. No, deny access

    ↑/↓ Navigate
  ```
- **选项语义与按键行为**：
  - **按键即时提交**：按单字符数字键 `1` 或 `4`，**无需按 Enter 键**，界面立即识别并提交！
  - **选项 2（会话级前缀放行）**：
    - 前缀粒度：放行规则针对的是选项文字中明确列出的前缀（对于具体命令通常是该完整命令前缀）。
    - 存储位置：**纯内存维持**。既**不写入** `transcript.jsonl`，也**不写入**全局 `settings.json`。会话退出后失效。
    - 作用范围：本会话内后续匹配该前缀的命令自动放行，其它前缀命令依然弹窗。
  - **选项 3（全局持久放行）**：
    - 「未验证（避免修改全局 settings.json）」。依据二进制内部字符串和报错信息分析，选项 3 会将对应规则写入全局 `settings.json` 的 `permissions.allow` 数组（格式形如 `command(<target>)`）。
  - **选项 4（No, cancel）vs `Escape` 键的核心区别**：
    - **选 `4`**：给模型一个明确的拒绝反馈。transcript 记录状态为 `ERROR` 的 `GENERIC` 步骤（`error: "permission check failed ... user denied permission to run command ..."`），模型获取到该错误并继续自愈解释。
    - **按 `Escape`**：直接触发**交互打断（Interrupt）**！CLI 清除 pending tool confirmation，**不会**在 transcript 中写入 `GENERIC` 错误步骤，直接取消模型生成并立刻回到输入框空闲状态。

### 3.4 MCP（Model Context Protocol）支持

- **通过 `--add-dir` 独立挂载**：
  - 实测确认：MCP 服务完全可以通过 `--add-dir <addon_dir>` 独立提供，工作区根目录下**完全无需任何 `.agents/` 目录或配置文件**！
  - 配置位置：`<addon_dir>/.agents/mcp_config.json`：
    ```json
    {
      "mcpServers": {
        "addon_mcp": {
          "command": "/path/to/mcp-server.py",
          "env": {
            "CUSTOM_VAR": "custom_val"
          }
        }
      }
    }
    ```
- **环境继承与自定义环境变量**：
  - 实测证实：MCP 服务子进程会**完整继承 CLI 进程的环境变量**（如 `RA_SESSION`）；
  - 同时，配置中的 `"env": { ... }` 也会正确注入到 MCP 服务的进程环境中。
- **握手与工具发现**：
  - `agy` 启动时自动完成 MCP 握手（`server/discover` → `initialize` → `notifications/initialized` → `tools/list`）。
  - 工具元数据缓存在 `~/.gemini/antigravity-cli/mcp/<ServerName>/<ToolName>.json`。
- **模型调用机制**：
  - 模型通过内置的 `call_mcp_tool` 派发：
    - 参数：`ServerName`（服务器名）、`ToolName`（工具名）、`Arguments`（参数字典）。
  - 系统在底层向 MCP 服务发送 `tools/call`，并在 `params._meta` 中注入：
    - `antigravity.google/artifacts_dir`
    - `antigravity.google/conversation_id`
    - `antigravity.google/root_conversation_id`
    - `progressToken`

## 4. 屏幕捕获与状态机

通过 tmux `capture-pane -p -e` 捕获的 13 种特征屏幕状态（测试数据见 `internal/antigravity/testdata/screen/`）：

| 状态名称 | 样本文件 | 核心特征行（Distinguishing Lines） |
|---|---|---|
| 空闲空输入框 | `idle-empty-box.txt` | 两条全宽横线 `───`，中间为亮蓝 `> ` 且无任何文字或占位符 |
| 单行草稿 | `draft-oneline.txt` | 横线中间为 `> ls -la`，包含输入的待提交文字 |
| 多行草稿 | `multiline-draft.txt` | 输入框自适应撑开多行，每行保留文本内容 |
| 忙碌运行中 | `busy-spinner.txt` | 横线上方显示点阵转圈及提示：`⣾ Working...` 或 `· Running command...` |
| 原生命令审批 | `permission-prompt.txt` | 包含 `Run this command?` 以及 4 个数字编号的选项行（`1. Yes, run command` ... `4. No, cancel`） |
| 原生文件审批 | 实测捕捉 | 包含 `Allow creation of this file?`，选项为 `1. Yes, allow creation`、`2. No, deny creation` |
| 提问（单选） | `ask-question-single.txt` | 横线上方显示 `? <问题文本>`，下方为数字选项列表（如 `1. Apple`、`2. Banana`） |
| 提问（多选） | `ask-question-multi.txt` | 包含复选框 `[ ]` 或 `[x]`，底部显示 `Select: Space/Number · Submit: Enter · Cancel: Esc` |
| 生成工件中的画面 | `artifact-review-notice.txt` | 样本实际仍为 `Loading...`，不是就绪通知；通知的文字证据见 §4.2 |
| 工件列表 | `artifact-review-screen.txt` | 样本实际显示 `Artifacts / No artifacts` 和 `esc Dismiss`，是空工件列表，不能作为计划待批准的证据 |
| 运行打断状态 | `interrupted.txt` | 包含横线提示行：`Interrupted · What should Antigravity CLI do instead?` |
| 消息排队指示 | `queued-message.txt` | 输入框上方显示 `1 queued message`，提示在忙碌时有待处理输入 |
| 工作区信任弹窗 | `trust-dialog.txt` | 居中显示 `Quick safety check: Is this a project you created or one you trust?`，光标停在 `> Yes, I trust this folder` |
| 模型选择菜单 | `model-picker.txt` | 弹出模型列表，高亮当前选中的模型（如 `❯ Gemini 3.8 Flash (Low)`） |

### 4.1 提问交互（ask_question）

- **单选模式（is_multi_select: false）**：
  - 用户按下对应数字键（`1`、`2`、`3`），**无需按 Enter**，界面立即高亮选中并瞬间提交。
  - transcript 中的 `GENERIC` 步骤记录答案文本：`A1: Banana`。
- **多选模式（is_multi_select: true）**：
  - 用户按数字键（`1`、`2`、`3`）或光标移动加 `Space` 键，用于切换选中状态（`[ ]` ↔ `[x]`）。
  - 若包含 `Write-in...` 选项，移动至该项或按对应数字键会激活内联文本框，允许用户自由输入字符串。
  - 用户按 `Enter` 键确认并提交所有勾选项与自定义文本。
  - transcript 中的 `GENERIC` 步骤记录组合结果，例如：`A1: Feature Beta, Custom Delta`。

### 4.2 工件审批（Plan / Artifact Review）

- 模型通过 `write_file` 生成带有 `RequestFeedback: true` 的工件。
- 终端显示通知 `1 artifact · /artifact to review`，按 `Ctrl-R`（或输入 `/artifact`）展开全屏审查：
  - 按 `y`：批准。系统自动注入 `<USER_REQUEST>\n[Approved] <file>\n</USER_REQUEST>`。
  - 按 `n`：拒绝。系统自动注入 `<USER_REQUEST>\n[Rejected] <file>\n</USER_REQUEST>`。

## 5. 输入注入、模式与审批语义矩阵

### 5.1 审批与模式行为矩阵（实测核对表）

| `toolPermission` 配置 | `--mode` 参数 | `read_file` | `write_file` (新建文件) | `run_command` (`echo` 等命令) | 行为特点 |
|---|---|---|---|---|---|
| **`always-proceed`** | default (无) | **免批** | **免批** | **免批** | 全自动执行，不弹任何确认 |
| **`always-proceed`** | `plan` | **免批** | **生成计划工件** | **免批** (按需) | 不直接修改代码，而是生成带反馈的计划工件 |
| **`request-review`** | default (无) | **免批** | **弹窗索权** (`Allow creation?`) | **弹窗索权** (`Run this command?`) | 读操作免批，写操作与命令执行均需人工确认 |
| **`request-review`** | `accept-edits` | **免批** | **免批** (自动批准编辑) | **弹窗索权** (`Run this command?`) | 文件编辑全免批，命令执行需确认 |
| **`strict`** | default (无) | **弹窗索权** (`Allow access?`) | **弹窗索权** (`Allow creation?`) | **弹窗索权** (`Run this command?`) | 最严级别，读文件、写文件、运行命令全弹窗 |
| **`proceed-in-sandbox`**| default (无) | **免批** | 沙箱内免批 | **非沙箱命令弹窗** | 限制非沙箱命令 |

### 5.2 `--mode auto` 在当前账号下的表现

- **启动警告**：在当前 Google AI Pro 账号下，以 `--mode auto` 启动时，CLI 在 stderr 打印：
  `warning: --mode auto is not available for this account; continuing in the default mode.`
- **交互式切换表现**：尽管启动给出警告，但在交互式终端中按 `Shift+Tab` 轮转时，**依然会轮转到 Auto 模式**，输入框占位符显示：
  `> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)`。

### 5.3 模式感知与 statusLine cycle_mode 对应关系

- **模式在 statusLine 中的暴露特征**：
  - `statusLine` JSON 中的 `cycle_mode` 字段直接反映当前交互模式：
    - **Default（默认模式）**：`cycle_mode` 字段在 JSON 中**完全缺省（omitted / absent）**，反序列化时为 `null`；终端输入框为标准空闲框 `> `，无占位文字。
    - **Accept-edits 模式**：`"cycle_mode": "accept-edits"`；输入框占位符显示 `> Accept-edits mode: file edits auto-approved (shift+tab to cycle)`。
    - **Auto 模式**：`"cycle_mode": "auto"`；输入框占位符显示 `> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)`。
    - **Plan 模式**：`"cycle_mode": "plan"`；输入框占位符显示 `> Plan mode: explore and plan without making edits (shift+tab to cycle)`。
  - **更新时延**：在交互终端中按下 `Shift+Tab` 时，`statusLine` 会**即时写入更新**，与输入框占位符渲染完全同步。

### 5.4 计划模式审批后状态流转（Plan Mode Transition）

- 当在 Plan 模式下生成计划工件并通过审查界面按 `y` 批准后：
- 退出审查界面时，**会话自动退出 Plan 模式，返回至 Default 模式**！
- 输入框内的 `> Plan mode: ...` 占位符自动清空，恢复为默认空闲输入框 `> `，statusLine 中的 `cycle_mode` 字段即时恢复为缺省（`null`）。

### 5.5 运行时权限设置与免批标志

- **`/permissions` 面板规格**：
  - 在 TUI 中输入 `/permissions` 打开 "Permission Config Editor" 面板，提供 3 种配置作用域（Scope）：
    1. **Project**：仅作用于当前项目的规则（最高优先级）；
    2. **Shared with Antigravity**：在 Antigravity 系列工具间共享的规则（保存至共享 `config.json`）；
    3. **Global**：跨所有项目的全局规则。
  - 该面板用于增删改查具体的粒度放行/拦截规则（如命令白名单 `command(...)`、路径规则、GitHub 作用域），**不提供**全局 `toolPermission` 档位切换。
- **`/settings`（或 `/config`）面板与 `toolPermission`**：
  - 在 TUI 中输入 `/settings` 或 `/config` 打开 20 项配置列表，其中第 18 项为 `Tool Permission`（包含 `always-proceed`、`request-review`、`strict`、`proceed-in-sandbox`）。
  - **持久化特征**：在全局 `~/.gemini/antigravity-cli/settings.json` 中，`toolPermission` 为顶层持久化键值。在 `/settings` 中修改 `Tool Permission` 推测会写回全局 `settings.json`，标记为「未验证（避免修改全局 settings.json）」。
- **`--dangerously-skip-permissions` 的不可逆性**：
  - 一旦启动时传入了 `--dangerously-skip-permissions`，虽然按 `Shift+Tab` 依然能改变输入框占位符文字，但底层所有工具调用依然会被 CLI 运行时标志强制直接放行，无法在会话中切回弹窗索权。

### 5.6 输入控制技术细节

- **草稿清理（Clear Draft）**：
  - 必须发送 `Ctrl-E`（跳至行尾）+ `Ctrl-U`（向前清除至行首），即：
    ```bash
    tmux send-keys -t <target> C-e C-u
    ```
- **打断（Interrupt）**：
  - 发送 `Esc` 或 `Ctrl-C` 立即中断当前工具或生成。中断不产生 `Stop` hook，终端进入 Interrupted 状态。

## 6. 模型、档位与配额桶分类

- **实测可用模型列表（`agy models` 精确输出）**：
  当前环境下 `agy models` 返回且仅返回以下 13 个合法模型标识：
  - `gemini-4-argon-low`
  - `gemini-4-argon-medium`
  - `gemini-4-argon-high`
  - `gemini-3.8-flash-low`
  - `gemini-3.8-flash-medium`
  - `gemini-3.8-flash-high`
  - `claude-opus-5-5-low`
  - `claude-opus-5-5-medium`
  - `claude-opus-5-5-high`
  - `claude-sonnet-5-5-low`
  - `claude-sonnet-5-5-medium`
  - `claude-sonnet-5-5-high`
  - `gpt-oss-120b-medium`

- **思考档位（Effort）规则**：
  - 模型 ID 通过 `-low`、`-medium`、`-high` 后缀编码。
  - 测试 `--effort xhigh` 报错退出：`reasoning effort "xhigh" is not supported for gemini-4-argon`。
  - 测试 `--effort max` 报错退出：`unrecognized reasoning effort "max"`。
  - 测试未知模型报错退出并列出上述合法列表。
  - 测试 `gpt-oss-120b` 仅支持 `medium`，显式指定 `low` 会报错退出。

- **交互式 `/model` 的全局持久化副作用**：
  - 在 TUI 中执行 `/model` 并完成选择后，**会永久改写 `~/.gemini/antigravity-cli/settings.json` 中的 `"model"` 字段**。
  - 实测 diff 证据：执行前后 `settings.json` 中的 `"model"` 键从 `"Gemini 3.8 Flash (Medium)"` 被直接修改为 `"Gemini 3.8 Flash (Low)"`。
  - **行为特征**：在运行中向 TUI 发送 `/model` 会产生全局持久化副作用；若需单会话生效，必须在启动进程时通过 `--model` 参数明确指定。

- **配额桶分类与模型家族对应关系**：
  通过 statusLine JSON 下发的 `quota` 对象包含 5 组独立的时间窗口桶：
  1. **`gemini-5h`**：Gemini 家族（`gemini-3.8-flash-*`、`gemini-4-argon-*`）的 5 小时滑动配额；
  2. **`gemini-weekly`**：Gemini 家族的自然周累计配额；
  3. **`gemini-argon-wtu-weekly`**：Gemini 4 Argon 专用的周 Workload Token Unit (WTU) 预算；
  4. **`3p-5h`**：第三方模型（Claude Opus 5.5、Claude Sonnet 5.5、GPT-OSS 120b）的 5 小时滑动配额；
  5. **`3p-weekly`**：第三方模型的自然周累计配额。
  每个桶提供 `remaining_fraction`（剩余比例浮点数）、`reset_time`（RFC3339 格式的 UTC 字符串，如 `2026-11-03T21:00:07Z`）与 `reset_in_seconds`（剩余秒数）。`reset_time` 不是整数秒时间戳。

## 7. 配额与状态栏（StatusLine）

- **底栏配额并非 agy 原生渲染**：
  - 本机 TUI 底部出现的 `5h` 和 `7d` 进度条，是由用户配置的自定义脚本 `~/.gemini/antigravity-cli/statusline.sh` 计算并打印的，**不是 Antigravity CLI 的原生底栏**。
  - 当通过 `--settings '{"statusLine": null}'` 禁用自定义脚本时，`agy` 原生底栏只显示极简的两项内容：
    - 左侧：`? for shortcuts`
    - 右侧：当前模型名称（如 `Gemini 3.8 Flash (Low)`）
    - 底栏**完全不包含**任何配额进度条、重置时间或上下文占用信息。因此不能使用屏幕截屏刮取（Screen Scraping）方式采集配额。

- **StatusLine JSON 生命周期与字段时序**：
  - **新会话启动瞬间（首轮回复前）**：
    - `conversation_id`: 为空字符串 `""`；
    - `transcript_path`: 为未带 UUID 的通用路径 `/.../brain/.system_generated/logs/transcript.jsonl`；
  - **首轮回复完成瞬间**：
    - `conversation_id`: 填充为正式分配的 UUID（如 `"201a0e61-cd9d-4bdd-823c-c99b2b507104"`）；
    - `transcript_path`: 变为指向具体会话目录的准确路径 `/.../brain/<UUID>/.system_generated/logs/transcript.jsonl`；
  - **恢复会话启动瞬间（`--conversation <id>`）**：
    - `conversation_id`: 从启动首个快照开始即为准确的 UUID；
    - `transcript_path`: 从启动首个快照开始即为准确路径。

- **StatusLine 输入 JSON 全量规格（实测字段列表）**：
  - `agent_state`: 当前状态（如 `"idle"`）。
  - `context_window`: 原始 JSON 的字段为 `context_window_size`、`total_input_tokens`、`total_output_tokens`、`used_percentage`、`remaining_percentage`、`current_usage`；`current_usage` 在还没调用模型时为 `null`。
  - `conversation_id`: 会话 UUID。
  - `cwd` / `workspace`: 当前工作区路径。
  - `cycle_mode`: 当前 Shift+Tab 轮转模式（`null`、`"accept-edits"`、`"auto"`、`"plan"`）。
  - `artifact_count`: 当前会话生成的工件总数。
  - `email` / `plan_tier`: 登录邮箱与订阅级别（`"Google AI Pro"`）。
  - `quota`: 包含上述 5 个配额桶的结构化对象。
  - `transcript_path`: 当前会话 transcript 的全路径。
  - `model`: 模型名称与标识。
  - `terminal_width`: 终端宽度。

## 8. 压缩与上下文（Compaction）

- **手动触发**：在输入框键入 `/compact` 并回车。
- **Transcript 记录结构**：
  - 压缩操作**绝对不会删除、截断或覆盖** `transcript.jsonl` 中此前的任何历史记录。
  - 压缩在日志尾部追加一条类型为 `type: "CHECKPOINT"`、`source: "SYSTEM"` 的新步骤。
  - `CHECKPOINT` 记录的 `content` 包含一份结构化 Markdown 总结，包括：
    - `# Resuming from a compaction`
    - `<summary>` 块（包含 Task Overview、Progress、Active Context、Next Steps、Commitments & Constraints）
    - 活跃工件列表与路径
  - 后续新步骤以连续递增的 `step_index` 继续追加在 `CHECKPOINT` 之后。

## 9. 子 Agent 与后台任务

- **子 Agent（Subagents）**：
  - `agy` 内置子代理体系，通过内置 `invoke_subagent` 工具派发。
  - 内置支持类型：`self`（继承当前上下文与配置）、`research`（只读调研沙箱）、`image-generator`。
  - 通信机制：通过 `send_message` 以 `conversationId` 寻址。子 Agent 完成时，系统向父 Agent 注入一条 `type: "SYSTEM_MESSAGE"` 记录将其唤醒。
- **后台命令管理**：
  - `run_command` 工具具备 `WaitMsBeforeAsync` 参数。若进程未在时限内退出，自动转入后台运行（分配 `taskId`，日志输出至 `.system_generated/tasks/task-*.log`）。
  - 内置 `manage_task` 工具支持 `list`、`status`、`send_input`、`kill`。
  - 进程重启后，未完成的后台任务被系统统一标记终止。

## 10. 错误恢复与边界条件

- **网络代理依赖**：
  - `agy` 依赖 Google 内部端点完成模型推理与配额同步。
  - 在受限网络环境下，拉起进程前必须注入 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY` 代理环境变量，否则将在握手阶段超时挂起。
- **排他锁（Presence Lock）**：
  - 正在运行的会话会在 `presence/<conversation-id>.lock` 维持文件锁。
  - 进程非正常退出时锁可能残留。恢复会话（`--conversation <id>`）时，若锁对应的 PID 已失效，`agy` 会自动回收并接管；若原进程仍在运行，则拒绝并发接管。
- **排错日志**：
  每次运行均在 `~/.gemini/antigravity-cli/log/` 生成独立日志，软链接 `~/.gemini/antigravity-cli/cli.log` 始终指向最新日志。

## 11. Antigravity CLI 特性速查表

| 特性维度 | Antigravity CLI (`agy`) 实测行为 |
|---|---|
| **CLI 二进制** | `agy`（Mach-O 64-bit arm64，位于 `~/.local/bin/agy`） |
| **首次工作区信任** | 交互式终端全屏弹窗，**光标默认落在 `> Yes, I trust this folder`** |
| **信任绕过方式** | 启动时传入 `--settings '{"trustedWorkspaces": ["<path>"]}'`，无需修改全局配置 |
| **外部指定会话 ID** | 不支持外部预设；但启动瞬间可在 `presence/<uuid>.lock` 获取新分配的 UUID |
| **会话恢复与分支** | `--conversation <uuid>` 原地追加恢复；`/fork` 独立克隆 SQLite 并新建隔离 transcript |
| **会话级 Hook 挂载** | 启动参数 `--add-dir <dir>` 挂载含有 `.agents/hooks.json` 的目录；实测该目录无需加入 `trustedWorkspaces`，主工作区受信任即可直接执行 |
| **会话级 MCP 挂载** | 启动参数 `--add-dir <dir>` 挂载含有 `.agents/mcp_config.json` 的目录；工作区根目录可保持完全干净 |
| **PreToolUse 拦截时序** | Hook 在原生确认弹窗之前执行；Hook 阻塞期间界面保持 busy，不弹原生窗口 |
| **PreToolUse 决策契约** | 仅支持 `allow`/`deny`/`ask`/`force_ask`；`approve` 为非法决策导致工具报错失败；输出空 `{}` 判定为拒绝 |
| **PreToolUse 原生审批** | `request-review` 下 Hook 输出 `allow` 或 `permissionOverrides` **均不能**压制原生权限弹窗；输出 `deny` 阻断 |
| **原生权限确认提示** | 命令行提供 4 选项菜单；按数字键 `1` 或 `4` **直接提交无需 Enter**；按 `Esc` 触发打断而非记录错误 |
| **写文件权限确认** | `write_file` 在 `request-review` 下提示 `Allow creation of this file? 1. Yes, allow creation  2. No, deny creation` |
| **读文件权限确认** | `read_file` 仅在 `strict` 模式下提示 `Allow access to this file? 1. Yes, allow access  2. No, deny access` |
| **模式体系与审批** | `--mode accept-edits` 自动批准编辑；`Shift+Tab` 循环切换；`--dangerously-skip-permissions` 强制免批且不可退回 |
| **Plan 模式审批后流转** | 计划工件经用户批准后，**自动退出 Plan 模式并回到 Default 模式**（占位符清空，`cycle_mode: null`） |
| **Transcript 日志机制** | 双文件实时追加（`transcript.jsonl` + `transcript_full.jsonl`），长命令运行期间实时写入，历史行不可变 |
| **history.jsonl 格式** | 包含 `display`（多行保存全量换行文本，不存占位符）、`timestamp`、`workspace`、`conversationId`、`type` |
| **图片与多媒体记录** | 存储在 `.system_generated/logs/media/step_X_media_0.png`，transcript 中以 `media` 数组记录文件 URI |
| **提问交互机制** | `ask_question` 工具：单选按数字键直接瞬间提交；多选按数字/空格切换，支持 Write-in 输入，按 Enter 提交 |
| **草稿清除序列** | 必须发送 `Ctrl-E` + `Ctrl-U`（行首单发 `Ctrl-U` 无法删除右侧字符） |
| **打断行为** | `Esc` 或 `Ctrl-C` 立即中断当前工具或生成；不触发 `Stop` hook，终端进入 Interrupted 状态 |
| **配额与指标采集** | 底栏 5h/7d 来自 statusline 脚本而非原生；通过 `--settings statusLine` 从 stdin 结构化 JSON 采集 5 组独立配额桶 |
| **可用模型范围** | `agy models` 严格限定 13 个模型（4 个家族各 low/medium/high，以及 gpt-oss-120b-medium） |
| **运行时改模副作用** | TUI 中执行 `/model` 会永久改写全局 `settings.json` 的 `"model"`；需由启动参数 `--model` 显式控制 |
| **上下文压缩处理** | `/compact` 在 transcript 尾部追加 `CHECKPOINT` 记录，保留完整 Markdown 摘要，历史记录不删除 |


## 12. 1.5.0 启动与输入补测（2026-11-04）

以下是 macOS、tmux 独立 socket、临时工作区中的直接 TUI 观察；本节启动、模式和草稿实验没有提交模型请求，未据此宣称完整工具交互验收通过。

- `agy --version` 在 1.5.0 只输出 `1.5.0`，没有 `Antigravity CLI` 前缀。
- 首条 prompt 之前，状态栏在登录信息拉取完成后已经能带 `quota`，`conversation_id` 仍为空。因此没有额度不等于 API key 登录，也不能保证额度一定要等第一条回复才出现。各桶的 `reset_time` 是 RFC3339 字符串，和前述原始 1.4.12 状态栏快照一致。
- `--mode plan` 的空输入框占位文字实测为 `Plan mode: research & plan only (shift+tab to cycle)`；Accept-edits 为 `Accept-edits mode: file edits auto-approved (shift+tab to cycle)`。占位文字使用 SGR 90（灰色），并非 SGR dim。去掉颜色时，相同的文字和用户手写的草稿无法仅凭截屏区分。最小原始样本见 `testdata/screen/mode-1.5.0-*.txt`。
- 真正的 bracketed paste（tmux `paste-buffer -p`）将 `line one\nline two` 留在同一个未提交的多行输入框，续行缩进两个空格；样本见 `testdata/screen/multiline-1.5.0-draft.txt`。旧的 `multiline-draft.txt` 含字面量 `^[[200~` 和已提交的第一行，只能证明那次粘贴转义失败，不能作为正确 bracketed paste 的证据。
- Ctrl-E Ctrl-U 只清除光标所在的行。多行草稿要逐行清除，用 Backspace 合并上一行后再重复；每次都可以捕获输入框确认，不能把一次按键成功当作整框已空。
- 在显式 `toolPermission: request-review` 的 1.5.0 隔离会话中，连续 Shift+Tab 的状态栏事件按 `Default → Accept-edits → Auto → Plan → Default` 出现；每格都读到了对应回报。启动时的 Auto 警告与交互循环能到达 Auto 并不矛盾，不能由警告推断运行中不可选。
- 原始 Hook 证据中的 `transcriptPath` 指向 `transcript_full.jsonl`。PreToolUse 的 `stepIdx` 对应工具结果 `GENERIC` 的 `step_index`，不是前一个 `PLANNER_RESPONSE` 的索引；transcript 的参数值可能是编码后的 JSON 字符串，hook 对应值则是原生字符串、数字或对象。按索引和规范化后的参数可以核对同一个调用。

- 同一天的原生状态栏补测还观察到：`gemini-5h.remaining_fraction` 在 `reset_time` 不变、尚未到该时间时从 0 回升到一个小的正值。因此该桶在同一个窗口中的已用比例并不保证单调递增；这里只记录变化，不推断服务端回补算法或可支撑多少轮请求。

- 空闲画面的状态栏命令不会保证反复执行。1.5.0 的独立会话中，把视口缩窄一列、再恢复原宽，均触发了新的状态栏命令输入，`terminal_width` 分别等于新宽和原宽；该操作没有输入 prompt 或切换模式。

- 1.5.0 的真实加密链路测试中，SessionStart 指向 `antigravity-cli/brain/<id>/...` 的实际日志，状态栏却给出 `antigravity/brain/<id>/...`；后者在该环境不存在，并非符号链接。两处 conversation_id 相同，前者已有 USER_INPUT 与实际模型回复。不能仅因为这两个路径字符串不同就认为切换了对话，读取时须核对实际 CLI 状态文件。

- 另一轮 1.5.0 新会话的 request-review 模式切换事件为 `Default → Accept-edits → Plan → Default`，没有收到 Auto 的确认帧；不能把某次完整循环固定当作所有会话都具备的模式列表。远程操作按每格实际回报确认，缺少目标就报告不可达，不由这一观察给整台电脑的模型打禁用标记。

- 1.5.0 的文件创建审批把绝对路径及 `+N` 行数放在 diff 标题上，随后是 1/2 两项菜单，没有命令审批的 `Requesting permission for` 标题。macOS 实测 hook/transcript 记录 `/tmp/...` 原文，原生 diff 标题显示规范化的 `/private/tmp/...`，两者完整参数一致且文件系统解析到同一个目标。命令文本仍须逐字核对，不能用这条文件路径规则替代命令匹配。

- 1.5.0 真实待审计划按 Ctrl-R 后是 `Action required (1 left)` 面板，不是普通 `Artifacts` 列表。选中行显示 `› □ new <文件名> open approve reject`，页脚明确提供 `y/n Approve/reject` 和 `esc Done`；已脱敏的真实样本见 `plan-review-1.5.0-pending.txt`。不能等待 `Artifacts` 这个标题来判断待审计划已打开。

- 1.5.0 的 Plan 模式实测把普通输入记录为 `/plan <原 prompt>`，屏幕和 USER_INPUT 中都带这个前缀；这不是只有 `/plan` 的模式命令。整行按 slash command 丢弃会漏掉真实用户消息及其提交确认。

- 1.5.0 的多工件审阅中，`y/n` 可能只把行标成 `approved/rejected`，并出现 `Submit / Dismiss` 按钮；此时还没有生成对应 USER_INPUT。把焦点移到原生 Submit 并回车后，才观察到 `[Approved]`/`[Rejected]` 和后续模型轮次。仅因选中行改变或不再显示 `new` 不能声称已交付审批；不能顺带提交其他工件尚未确认的选择。
