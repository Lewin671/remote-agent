// Development only: a Remote with made-up machines and sessions in every
// state docs/design/client-v1.html draws, and no server. Opened with ?demo
// on the dev server; builds leave this file out. Options:
//   ?demo&conn=connecting|reconnecting|fail   the server link's states
//   ?demo&term=closed                        terminals that drop at once
//   ?demo&empty=1 | empty=offline | empty=none  accounts with no sessions
//   ?demo&list=fail                          the first listing fails

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import type { Keys } from "../crypto";
import {
  Agent,
  ApprovalDecision,
  ApprovalMode,
  EventSchema,
  InputStatus,
  SessionStatus,
  ToolKind,
  TurnOutcome,
  type Event,
} from "../gen/remoteagent/v1/event_pb";
import { DaemonSettingsSchema, ListModelsResponseSchema, SessionSchema, type Session } from "../gen/remoteagent/v1/local_pb";
import { CommandResultSchema, CommandSchema, MachineMetaSchema, type CommandResult, type TerminalFrame } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, Remote, type CommandKind, type History, type RequestSlot, type SessionView, type TermConn } from "../remote";

type EventKind = MessageInitShape<typeof EventSchema>["kind"];
type SessionInit = MessageInitShape<typeof SessionSchema>;

const now = Date.now();
const min = 60_000;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const ctxAuto = { approvalsReviewer: "auto_review", approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: "default" };
const ctxAsk = { approvalsReviewer: "user", approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: "default" };
const ctxPlan = { ...ctxAsk, collaborationMode: "plan" };

const answer = [
  "问题在 [auth/login.go:58](/Users/me/Code/foo/auth/login.go:58)：",
  "",
  "- 每次登录都新建 Redis 连接",
  "- 连接没设超时，池子耗尽后请求挂住",
  "",
  "我改成复用连接池并加上 `2s` 超时。详见 [Go 文档](https://pkg.go.dev/context)。",
].join("\n");

const long = [
  "对比如下：",
  "",
  "```go",
  "func Login(ctx context.Context, req LoginRequest) (*Session, error) {",
  "```",
  "",
  "| 依赖 | 当前 | 最新 | 说明 |",
  "| --- | --- | --- | --- |",
  "| pgx | v4.18.3 | v5.7.1 | 大版本，要改连接池 |",
  "| chi | v5.0.12 | v5.2.0 | 小版本 |",
  "",
  "1. 先升小版本",
  "2. 再单独处理 pgx",
].join("\n");

interface Fixture {
  info: SessionInit;
  machine: string;
  updated: number;
  events: EventKind[];
}

function fixtures(): Record<string, Fixture> {
  return {
    "s-run": {
      machine: "mac",
      updated: now - 20_000,
      info: { title: "修复登录超时", cwd: "/Users/me/Code/foo", status: SessionStatus.RUNNING, model: "gpt-6-astra", effort: "medium", ...ctxAuto, contextTokens: 87_000n, contextWindow: 760_000n, preview: "正在执行 go test ./auth/..." },
      events: [
        { case: "userMessage", value: { text: "登录接口偶尔超时，帮我查一下原因并修掉" } },
        { case: "turnStarted", value: {} },
        { case: "reasoning", value: { text: "先看登录的实现，再看连接池的配置。" } },
        { case: "toolCall", value: { title: "rg -n Login auth", kind: ToolKind.COMMAND, status: "completed", exitCode: 0, output: "auth/login.go:56:func Login(" } },
        { case: "toolCall", value: { title: "sed -n 40,80p auth/login.go", kind: ToolKind.COMMAND, status: "completed", exitCode: 0 } },
        { case: "toolCall", value: { title: "cat auth/redis.go", kind: ToolKind.COMMAND, status: "completed", exitCode: 0 } },
        { case: "agentMessage", value: { text: answer } },
        { case: "toolCall", value: { title: "/Users/me/Code/foo/auth/login.go", kind: ToolKind.FILE_CHANGE, status: "completed" } },
        { case: "toolStarted", value: { title: "go test ./auth/..." } },
        { case: "inputState", value: { requestId: "q-run", status: InputStatus.QUEUED } },
      ],
    },
    "s-appr": {
      machine: "vps",
      updated: now - 10 * min,
      info: { title: "重构 API 层", cwd: "/Users/me/Code/foo", status: SessionStatus.RUNNING, pendingApprovals: 2, model: "gpt-6-astra", effort: "medium", ...ctxAsk, contextTokens: 23n, contextWindow: 100n },
      events: [
        { case: "userMessage", value: { text: "把 handler 拆到单独的包里" } },
        { case: "turnStarted", value: {} },
        { case: "toolCall", value: { title: "/Users/me/Code/foo/internal/handler/api.go", kind: ToolKind.FILE_CHANGE, status: "completed" } },
        { case: "approvalRequested", value: { approvalId: "a0", command: "mkdir -p internal/handler", toolName: "shell" } },
        { case: "approvalResolved", value: { approvalId: "a0", decision: ApprovalDecision.ALLOW } },
        { case: "agentMessage", value: { text: "拆分完成，跑一下测试确认没有破坏现有行为。" } },
        {
          case: "approvalRequested",
          value: {
            approvalId: "a1",
            toolName: "shell",
            description:
              "跑一遍全部测试，确认拆包没有破坏行为。先清掉旧的生成文件再用 buf 重新生成，编译通过后只跑配对、会话和终端三组测试，最后看一眼 gen 目录有没有意外的改动；任何一步失败都会停下来，不会继续执行后面的命令。",
            command: "rm -rf gen/remoteagent && \\\nbuf generate --template buf.gen.yaml && \\\ngo build ./... && \\\ngo test ./internal/... -count=1 -run 'Test(Pair|Session|Terminal)' && \\\ngit status --short gen/",
            deadline: timestampFromMs(now + 8 * min),
          },
        },
        { case: "approvalRequested", value: { approvalId: "a2", toolName: "apply_patch", description: "修改工作区外的文件", toolInputJson: '{"path": "/etc/hosts", "patch": "@@ -1,3 +1,4 @@"}', deadline: timestampFromMs(now + 6 * min) } },
      ],
    },
    "s-ask": {
      machine: "imac",
      updated: now - 21 * min,
      info: { title: "升级依赖", cwd: "/Users/me/Code/bar", status: SessionStatus.RUNNING, pendingQuestions: 1, model: "gpt-6-astra", effort: "medium", ...ctxAuto },
      events: [
        { case: "userMessage", value: { text: "把依赖都升级到最新的版本" } },
        { case: "turnStarted", value: {} },
        { case: "toolCall", value: { title: "go list -m -u all", kind: ToolKind.COMMAND, status: "completed", exitCode: 0 } },
        { case: "agentMessage", value: { text: "有 12 个依赖可以升级，其中 `pgx` 是 v4 → v5 的大版本。" } },
        {
          case: "questionAsked",
          value: {
            questionId: "q1",
            questions: [
              {
                text: "大版本也一起升吗？",
                options: [
                  { label: "只升小版本 (Recommended)", description: "改动小，go build 大概率直接通过" },
                  { label: "包括 pgx v5", description: "要改连接池和事务相关的调用" },
                ],
              },
              { text: "要顺便更新 go.mod 的 Go 版本吗？", options: [{ label: "更新到 1.27" }, { label: "保持不变" }] },
            ],
          },
        },
        { case: "inputState", value: { requestId: "q-ask", status: InputStatus.QUEUED } },
      ],
    },
    "s-claude": {
      machine: "mac",
      updated: now - 30 * min,
      info: { title: "给登录接口加限流", cwd: "/Users/me/Code/foo", status: SessionStatus.RUNNING, pendingQuestions: 1, agent: Agent.CLAUDE, model: "claude-sonnet-5-5", effort: "high", ...ctxAsk },
      events: [
        { case: "userMessage", value: { text: "给登录接口加限流" } },
        { case: "turnStarted", value: {} },
        { case: "toolStarted", value: { title: "Read auth/login.go", callId: "t1" } },
        { case: "toolStarted", value: { title: "Read auth/limit.go", callId: "t2" } },
        { case: "toolStarted", value: { title: "Grep RateLimit", callId: "t3" } },
        { case: "toolCall", value: { title: "Read auth/limit.go", callId: "t2", kind: ToolKind.OTHER, status: "completed" } },
        { case: "questionAsked", value: { questionId: "cq", questions: [{ text: "限流按 IP 还是按账户？", options: [{ label: "按 IP" }, { label: "按账户" }] }] } },
      ],
    },
    "s-claude-idle": {
      machine: "mac",
      updated: now - 35 * min,
      info: { title: "整理错误码", cwd: "/Users/me/Code/bar", status: SessionStatus.IDLE, agent: Agent.CLAUDE, model: "claude-sonnet-5-5", effort: "medium", ...ctxAsk, preview: "错误码已按模块分组" },
      events: [
        { case: "userMessage", value: { text: "整理错误码" } },
        { case: "agentMessage", value: { text: "错误码已按模块分组。" } },
      ],
    },
    "s-plan": {
      machine: "imac",
      updated: now - 40 * min,
      info: { title: "拆分订单服务", cwd: "/Users/me/Code/shop", status: SessionStatus.IDLE, planOpen: true, model: "gpt-6-sol", effort: "high", ...ctxPlan },
      events: [
        { case: "turnContext", value: { ...ctxAsk, model: "gpt-6-sol", effort: "high" } },
        { case: "turnContext", value: { ...ctxPlan, model: "gpt-6-sol", effort: "high" } },
        { case: "userMessage", value: { text: "先出个方案，把订单服务里的库存逻辑拆出去" } },
        { case: "toolCall", value: { title: "读取 14 个文件", kind: ToolKind.OTHER, status: "completed" } },
        { case: "agentMessage", value: { text: "方案如下，批准后开始改。" } },
        {
          case: "planProposed",
          value: { text: "1. 新建 `inventory` 包，迁移库存扣减和回滚\n2. 订单服务改为调用接口，保留旧实现做开关\n3. 补充集成测试，覆盖并发扣减\n4. 灰度一周后删掉旧实现\n5. 更新文档和监控面板" },
        },
      ],
    },
    "s-popup": {
      machine: "imac",
      updated: now - 50 * min,
      info: { title: "", cwd: "/Users/me/Code/billing", status: SessionStatus.BLOCKED, statusDetail: "Trust this folder?", createdAt: timestampFromMs(new Date().setHours(11, 25, 0, 0)), model: "gpt-6-astra", effort: "medium" },
      events: [{ case: "inputState", value: { requestId: "q-popup", status: InputStatus.QUEUED } }],
    },
    "s-idle": {
      machine: "mac",
      updated: now - 90 * min,
      info: { title: "补充接口文档", cwd: "/Users/me/Code/foo", status: SessionStatus.IDLE, preview: "已补充 12 个接口的说明，要生成 OpenAPI 吗？", model: "gpt-6-astra", effort: "medium", ...ctxAsk },
      events: [
        { case: "userMessage", value: { text: "补充接口文档" } },
        { case: "agentMessage", value: { text: "已补充 12 个接口的说明，要生成 OpenAPI 吗？" } },
      ],
    },
    "s-fresh": {
      machine: "imac",
      updated: now - 100 * min,
      info: { title: "", cwd: "/Users/me/Code/billing", status: SessionStatus.IDLE, createdAt: timestampFromMs(new Date().setHours(11, 25, 0, 0)), model: "gpt-6-astra", effort: "medium", ...ctxAsk },
      events: [],
    },
    "s-ctx": {
      machine: "mac",
      updated: now - 120 * min,
      info: { title: "重写支付模块", cwd: "/Users/me/Code/shop", status: SessionStatus.IDLE, model: "gpt-6-sol", effort: "high", ...ctxAuto, contextTokens: 86n, contextWindow: 100n },
      events: [
        { case: "tokenUsage", value: { lastTotalTokens: 91n, contextWindow: 100n } },
        { case: "settingsChanged", value: { compacted: true } },
        { case: "tokenUsage", value: { lastTotalTokens: 14n, contextWindow: 100n } },
        { case: "turnStarted", value: {} },
        { case: "toolCall", value: { title: "编辑 9 个文件  +412 −288", kind: ToolKind.OTHER, status: "completed" } },
        { case: "agentMessage", value: { text: "支付回调和退款流程都已迁移到新接口，测试全部通过。" } },
        { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED, durationMs: 252_000n } },
        { case: "userMessage", value: { text: "继续把对账逻辑也迁过去" } },
      ],
    },
    "s-msg": {
      machine: "mac",
      updated: now - 150 * min,
      info: { title: "消息的各种状态", cwd: "/Users/me/Code/foo", status: SessionStatus.IDLE, model: "gpt-6-astra", effort: "medium", ...ctxAsk },
      events: [
        { case: "agentMessage", value: { text: long } },
        { case: "toolCall", value: { title: "go build ./...", kind: ToolKind.COMMAND, status: "failed", exitCode: 1, output: "./auth/login.go:58:9: undefined: pool\n" + Array.from({ length: 12 }, (_, i) => `./auth/x.go:${i}: error`).join("\n") } },
        { case: "inputState", value: { requestId: "o-unconf", status: InputStatus.UNCONFIRMED } },
        { case: "approvalRequested", value: { approvalId: "r1", command: "rm -rf build/" } },
        { case: "approvalResolved", value: { approvalId: "r1", decision: ApprovalDecision.TIMEOUT } },
        { case: "approvalRequested", value: { approvalId: "r2", command: "curl https://example.com/install.sh" } },
        { case: "approvalResolved", value: { approvalId: "r2", decision: ApprovalDecision.DENY } },
        { case: "approvalRequested", value: { approvalId: "r3", command: "npm install" } },
        { case: "approvalResolved", value: { approvalId: "r3", decision: ApprovalDecision.CANCELED } },
      ],
    },
    "s-hist": {
      machine: "mac",
      updated: now - 3 * 60 * min,
      info: { title: "历史读取失败", cwd: "/Users/me/Code/foo", status: SessionStatus.IDLE, model: "gpt-6-astra", effort: "medium" },
      events: [{ case: "userMessage", value: { text: "这之后的记录读不到" } }],
    },
    "s-ended": {
      machine: "mac",
      updated: now - 2 * 24 * 60 * min,
      info: { title: "介绍下这个项目", cwd: "/Users/me/Code/ai-bot/remote-agent", status: SessionStatus.EXITED, preview: "没有按时间失效。模型列表缓存在前端页面的内存里…", model: "gpt-6-astra", effort: "medium" },
      events: [
        { case: "userMessage", value: { text: "缓存是多久失效" } },
        {
          case: "agentMessage",
          value: {
            text: "没有按时间失效。模型列表缓存在前端页面的内存里，**同一台机器每次页面加载只请求一次**；刷新或重新打开页面会重新获取。请求失败时会立即清除缓存（[web/src/remote.ts:612](/Users/me/Code/ai-bot/remote-agent/web/src/remote.ts:612)），下次调用会重试。",
          },
        },
        { case: "sessionState", value: { status: SessionStatus.EXITED, detail: "tmux session gone" } },
      ],
    },
    "s-ended2": {
      machine: "mac",
      updated: now - 26 * 60 * min,
      info: { title: "补充 README", cwd: "/Users/me/Code/foo", status: SessionStatus.EXITED, preview: "已补充安装步骤" },
      events: [],
    },
    "s-ended3": {
      machine: "vps",
      updated: now - 3 * 24 * 60 * min,
      info: { title: "升级 CI", cwd: "/Users/me/Code/foo", status: SessionStatus.EXITED, preview: "CI 改用 Go 1.27" },
      events: [],
    },
    "s-off": {
      machine: "mini",
      updated: now - 2 * 60 * min,
      info: { title: "对账脚本", cwd: "/Users/me/Code/billing", status: SessionStatus.IDLE, preview: "已导出到 out/2026-03.csv，共 1,284 行。", model: "gpt-6-astra", effort: "medium" },
      events: [
        { case: "userMessage", value: { text: "把 3 月的对账结果导出成 CSV" } },
        { case: "toolCall", value: { title: "python scripts/export.py --month 2026-03", kind: ToolKind.COMMAND, status: "completed", exitCode: 0 } },
        { case: "agentMessage", value: { text: "已导出到 [out/2026-03.csv](/Users/me/Code/billing/out/2026-03.csv)，共 1,284 行。" } },
      ],
    },
  };
}

const models = create(ListModelsResponseSchema, {
  defaultModel: "gpt-6-astra",
  models: [
    { slug: "gpt-6-astra", displayName: "GPT-6-Astra", efforts: ["low", "medium", "high", "xhigh", "max", "ultra"], defaultEffort: "medium" },
    { slug: "gpt-6-sol", displayName: "GPT-6-Sol", efforts: ["low", "medium", "high", "xhigh", "max", "ultra"], defaultEffort: "high" },
    { slug: "gpt-6-luna", displayName: "GPT-6-Luna", efforts: ["low", "medium", "high", "xhigh", "max"], defaultEffort: "low" },
    { slug: "gpt-6-nova", displayName: "GPT-6-Nova", efforts: ["low", "medium"], defaultEffort: "low" },
  ],
});
const claudeModels = create(ListModelsResponseSchema, {
  defaultModel: "sonnet",
  models: [
    { slug: "sonnet", displayName: "Sonnet", efforts: ["low", "medium", "high"], defaultEffort: "medium" },
    { slug: "opus", displayName: "Opus", efforts: ["low", "medium", "high"], defaultEffort: "high" },
  ],
});

const tree: Record<string, string[]> = {
  "/Users/me": ["Code", "Documents", "Downloads"],
  "/Users/me/Code": ["ai-bot", "foo", "bar", "billing", "shop"],
  "/Users/me/Code/ai-bot": ["remote-agent", "happy", "notes-sync"],
  "/Users/me/Code/foo": ["cmd", "internal"],
};

const screen = (title: string) =>
  [
    "\x1b[2m• Explored\x1b[0m",
    "  \x1b[2m└\x1b[0m Search func Login in auth · Read login.go, redis.go",
    "",
    "\x1b[2m• Edited\x1b[0m auth/login.go (+14 -6)",
    "    56     func Login(ctx context.Context, req LoginRequest) (*Session, error) {",
    "    57 \x1b[33m-       conn, err := redis.Dial(\"tcp\", cfg.RedisAddr)\x1b[0m",
    "    58 \x1b[32m+       conn := pool.Get()\x1b[0m",
    "",
    `\x1b[2m• Running\x1b[0m ${title}`,
    "",
    "\x1b[34m◦ Working (12s • esc to interrupt)\x1b[0m",
    "",
    "› ",
  ].join("\r\n");

const trust = ["\x1b[33m  Folder access\x1b[0m", "  ~/Code/billing", "", "  Trust this folder? Codex can", "  read, edit, and run files here.", "", "\x1b[34m› 1. Trust and continue\x1b[0m", "  2. Quit", "", "  enter continue · esc quit"].join("\r\n");

export class DemoRemote extends Remote {
  private evs = new Map<string, Event[]>();
  private attempts = new Map<string, number>();

  static create(q: URLSearchParams): DemoRemote {
    const r = new DemoRemote(
      { server: "https://ra.example.com", root: {} as CryptoKey, devicePrivate: {} as CryptoKey, deviceId: "demo" },
      {} as Keys,
    );
    r.opts = q;
    r.seed();
    return r;
  }

  private opts = new URLSearchParams();

  private seed() {
    const empty = this.opts.get("empty");
    const machine = (id: string, hostname: string, online: boolean, meta: MessageInitShape<typeof MachineMetaSchema> = {}) =>
      this.machines.set(id, { id, online, lastSeenMs: BigInt(online ? now : now - 2 * 60 * min), metaRev: 1n, meta: create(MachineMetaSchema, { hostname, home: "/Users/me", ...meta }) });
    if (empty !== "none") {
      machine("mac", "MacBook", empty !== "offline", {
        codexVersion: "0.71.0",
        codexVerified: false,
        planType: "Pro Lite",
        primaryLimit: { usedPercent: 62, windowMinutes: 300n, resetsAt: BigInt(Math.floor((now + 3 * 60 * min) / 1000)) },
        secondaryLimit: { usedPercent: 34, windowMinutes: 10080n, resetsAt: BigInt(Math.floor((now + 3 * 24 * 60 * min) / 1000)) },
        agents: [
          { agent: Agent.CODEX, version: "0.71.0", verified: false },
          { agent: Agent.CLAUDE, version: "2.3.1", verified: true },
        ],
      });
      if (!empty) {
        machine("vps", "VPS-dev", true, { codexVersion: "0.70.2", codexVerified: true });
        machine("imac", "iMac", true, { codexVersion: "0.71.0", codexVerified: true });
        machine("mini", "Mac mini", false);
      }
    }
    if (empty) return;
    for (const [id, f] of Object.entries(fixtures())) {
      const info: Session = create(SessionSchema, { id, createdAt: timestampFromMs(f.updated - 5 * min), ...f.info });
      this.sessions.set(id, { id, machineId: f.machine, key: {} as CryptoKey, info, metaRev: 1n, lastSeq: BigInt(f.events.length), updatedMs: BigInt(f.updated) });
      this.evs.set(
        id,
        f.events.map((kind, i) => create(EventSchema, { seq: BigInt(i + 1), sessionId: id, time: timestampFromMs(f.updated - (f.events.length - i) * 20_000), kind })),
      );
    }
    const out = (sid: string, sendId: string, text: string, status: "sending" | "sent" | "failed", error?: string) => {
      this.outboxes.set(sid, [...this.outbox(sid), { sendId, requestId: "r-" + sendId, deadline: now + 60_000, text, status, error }]);
    };
    out("s-run", "q-run", "顺便补一个测试", "sent");
    out("s-ask", "q-ask", "跑一下测试", "sent");
    out("s-popup", "q-popup", "看下这些脚本是做什么的", "sent");
    out("s-msg", "o-unconf", "把 lint 也跑一下", "sent");
    out("s-msg", "o-sent", "再看下 README 要不要改", "sent");
    out("s-msg", "o-fail", "顺便补一个测试", "failed", "连接超时");
    out("s-msg", "o-sending", "最后提交", "sending");
  }

  override start() {
    const c = this.opts.get("conn");
    this.listed = this.opts.get("list") !== "fail";
    if (!this.listed) this.listError = "连接超时";
    if (c === "connecting") this.conn = "connecting";
    else if (c === "reconnecting") Object.assign(this, { conn: "offline", everOnline: true });
    else if (c === "fail") Object.assign(this, { conn: "offline", failures: 3, connError: "证书无效" });
    else Object.assign(this, { conn: "online", everOnline: true });
    this.changed();
  }

  override stop() {}

  override retryList() {
    this.listError = "";
    this.changed();
    setTimeout(() => {
      this.listed = true;
      this.changed();
    }, 800);
  }

  override events(sid: string): Event[] {
    let e = this.evs.get(sid);
    if (!e) this.evs.set(sid, (e = []));
    return e;
  }

  override loaded(): boolean {
    return true;
  }

  override history(sid: string): History {
    return { loaded: true, error: sid === "s-hist" ? "连接超时" : "", broken: sid === "s-ended" };
  }

  override retryEvents() {}

  private push(sid: string, kind: EventKind) {
    const list = this.events(sid);
    list.push(create(EventSchema, { seq: BigInt(list.length + 1), sessionId: sid, time: timestampFromMs(Date.now()), kind }));
    const s = this.sessions.get(sid);
    if (s) s.updatedMs = BigInt(Date.now());
    this.changed();
  }

  private patch(sid: string, p: Partial<Session>) {
    const s = this.sessions.get(sid);
    if (s?.info) s.info = { ...s.info, ...p };
    this.changed();
  }

  override async command(sid: string, kind: CommandKind, slot?: RequestSlot): Promise<CommandResult> {
    await sleep(700);
    const cmd = create(CommandSchema, { kind }).kind;
    const ok = (kind: MessageInitShape<typeof CommandResultSchema>["kind"] = { case: undefined }) => create(CommandResultSchema, { kind });
    const s = this.sessions.get(sid)!;
    switch (cmd.case) {
      case "send": {
        const { text, requestId } = cmd.value;
        // Text with 失败 fails once; the retry with the same slot succeeds.
        const n = (this.attempts.get(requestId!) ?? 0) + 1;
        this.attempts.set(requestId!, n);
        if (text?.includes("失败") && n === 1) throw new CommandError("unknown", "连接超时");
        void slot;
        this.push(sid, { case: "inputState", value: { requestId, status: InputStatus.QUEUED } });
        void (async () => {
          await sleep(1500);
          if (text?.includes("终端")) return this.push(sid, { case: "inputState", value: { requestId, status: InputStatus.UNCONFIRMED } });
          this.push(sid, { case: "inputState", value: { requestId, status: InputStatus.SUBMITTED } });
          this.push(sid, { case: "userMessage", value: { text } });
          this.push(sid, { case: "turnStarted", value: {} });
          this.patch(sid, { status: SessionStatus.RUNNING });
          await sleep(2000);
          this.push(sid, { case: "agentMessage", value: { text: "收到。这是演示数据里的回复。" } });
          this.push(sid, { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED, durationMs: 2000n } });
          this.patch(sid, { status: SessionStatus.IDLE });
        })();
        return ok();
      }
      case "resolveApproval": {
        const v = cmd.value;
        if (v.message?.includes("失败")) throw new CommandError("unknown", "连接超时");
        this.push(sid, { case: "approvalResolved", value: { approvalId: v.approvalId, decision: v.allow ? ApprovalDecision.ALLOW : ApprovalDecision.DENY, message: v.message } });
        this.patch(sid, { pendingApprovals: Math.max(0, (s.info?.pendingApprovals ?? 1) - 1) });
        return ok();
      }
      case "answerQuestion": {
        const q = this.events(sid).find((e) => e.kind.case === "questionAsked" && e.kind.value.questionId === cmd.value.questionId);
        const qa = q?.kind.case === "questionAsked" ? q.kind.value : undefined;
        if (qa && (cmd.value.questionIndex ?? 0) >= qa.questions.length - 1) {
          this.push(sid, { case: "questionAnswered", value: { questionId: cmd.value.questionId, answers: [{ answers: ["已选"] }] } });
          this.patch(sid, { pendingQuestions: 0 });
        }
        return ok();
      }
      case "answerPlan":
        this.patch(sid, { planOpen: false, collaborationMode: "default" });
        return ok();
      case "cancelInput":
        this.push(sid, { case: "inputState", value: { requestId: cmd.value.requestId, status: InputStatus.CANCELED } });
        return ok();
      case "interrupt":
        this.push(sid, { case: "turnEnded", value: { outcome: TurnOutcome.ABORTED } });
        this.patch(sid, { status: SessionStatus.IDLE });
        return ok();
      case "stopSession":
        this.push(sid, { case: "sessionState", value: { status: SessionStatus.EXITED, detail: "stopped" } });
        this.patch(sid, { status: SessionStatus.EXITED, statusDetail: "stopped" });
        return ok();
      case "resumeSession":
        await sleep(1500);
        if (sid === "s-ended3") throw new CommandError("failed_precondition", "目录 ~/Code/foo 不存在了");
        this.patch(sid, { status: SessionStatus.STARTING });
        await sleep(1500);
        this.patch(sid, { status: SessionStatus.IDLE });
        return ok();
      case "setModel":
        if (cmd.value.model === "gpt-6-nova") throw new CommandError("unknown", "Codex 的菜单和预期不一样");
        this.push(sid, { case: "settingsChanged", value: { model: cmd.value.model, effort: cmd.value.effort } });
        this.patch(sid, { model: cmd.value.model, effort: cmd.value.effort });
        return ok();
      case "setApprovalMode": {
        const mode = cmd.value.mode;
        this.push(sid, { case: "settingsChanged", value: { mode } });
        const ctx = mode === ApprovalMode.AUTO ? ctxAuto : mode === ApprovalMode.PLAN ? ctxPlan : mode === ApprovalMode.FULL_ACCESS ? { ...ctxAsk, approvalPolicy: "never", sandbox: "danger-full-access" } : ctxAsk;
        this.patch(sid, ctx);
        return ok();
      }
      case "compact":
        await sleep(1500);
        this.push(sid, { case: "settingsChanged", value: { compacted: true } });
        this.patch(sid, { contextTokens: 14n, contextWindow: 100n });
        return ok();
    }
    return ok();
  }

  override async machineCommand(machineId: string, kind: CommandKind): Promise<CommandResult> {
    await sleep(500);
    const cmd = create(CommandSchema, { kind }).kind;
    const m = this.machines.get(machineId);
    if (!m?.online) throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    switch (cmd.case) {
      case "listModels":
        if (machineId === "vps") throw new CommandError("unknown", "连接超时");
        return create(CommandResultSchema, { kind: { case: "listModels", value: cmd.value.agent === Agent.CLAUDE ? claudeModels : models } });
      case "getSettings":
        return create(CommandResultSchema, { kind: { case: "getSettings", value: { settings: this.settingsFor() } } });
      case "updateSettings":
        if (cmd.value.settings?.approvalTimeoutSeconds === 3000) throw new CommandError("unavailable", `${m.meta?.hostname} 不在线`);
        this.saved = create(DaemonSettingsSchema, cmd.value.settings);
        return create(CommandResultSchema, { kind: { case: "updateSettings", value: { settings: this.saved } } });
      case "listDirectories": {
        const path = cmd.value.path || "/Users/me";
        if (path.endsWith("Downloads")) throw new CommandError("permission_denied", "没有读取权限");
        const parent = path === "/" ? "" : path.replace(/\/[^/]+$/, "") || "/";
        return create(CommandResultSchema, { kind: { case: "listDirectories", value: { path, parent, home: "/Users/me", dirs: tree[path] ?? [] } } });
      }
      case "createSession": {
        const v = cmd.value;
        if (v.prompt?.includes("失败")) throw new CommandError("failed_precondition", "tmux 没有安装");
        const id = "s-new-" + Math.random().toString(16).slice(2, 8);
        const info = create(SessionSchema, { id, cwd: v.cwd, status: SessionStatus.STARTING, title: v.prompt?.split("\n")[0] ?? "", agent: v.agent, model: v.model, effort: v.effort, createdAt: timestampFromMs(Date.now()) });
        const view: SessionView = { id, machineId, key: {} as CryptoKey, info, metaRev: 1n, lastSeq: 0n, updatedMs: BigInt(Date.now()) };
        setTimeout(() => {
          this.sessions.set(id, view);
          if (v.prompt) this.push(id, { case: "inputState", value: { requestId: "p-" + id, status: InputStatus.QUEUED } });
          this.changed();
        }, 300);
        setTimeout(() => {
          this.patch(id, { status: SessionStatus.IDLE, model: "gpt-6-astra", effort: "medium", ...ctxAsk });
          if (v.prompt) {
            this.push(id, { case: "inputState", value: { requestId: "p-" + id, status: InputStatus.SUBMITTED } });
            this.push(id, { case: "userMessage", value: { text: v.prompt } });
          }
        }, 4000);
        return create(CommandResultSchema, { kind: { case: "createSession", value: { session: info } } });
      }
    }
    return create(CommandResultSchema, {});
  }

  private saved?: ReturnType<typeof create<typeof DaemonSettingsSchema>>;

  private settingsFor() {
    return (this.saved ??= create(DaemonSettingsSchema, {
      approvalTimeoutSeconds: 600,
      defaultModel: "gpt-6-astra",
      defaultEffort: "medium",
      defaultMode: ApprovalMode.AUTO,
      claude: { model: "sonnet", effort: "high", mode: ApprovalMode.ASK },
    }));
  }

  override async openTerminal(sid: string, onFrame: (f: TerminalFrame) => void, onClose: (reason: string) => void): Promise<TermConn> {
    await sleep(400);
    let open = true;
    const popup = this.sessions.get(sid)?.info?.status === SessionStatus.BLOCKED;
    setTimeout(() => {
      if (!open) return;
      const data = new TextEncoder().encode(popup ? trust : screen(this.sessions.get(sid)?.info?.title ?? ""));
      onFrame({ $typeName: "remoteagent.v1.TerminalFrame", kind: { case: "snapshot", value: { $typeName: "remoteagent.v1.TerminalSnapshot", data, cols: popup ? 42 : 120, rows: popup ? 30 : 40 } } });
      if (this.opts.get("term") === "closed") setTimeout(() => open && onClose("stream closed: output backlog"), 800);
    }, 200);
    return {
      send: () => undefined,
      close: () => {
        open = false;
      },
    };
  }

  override async removeDevice(id: string) {
    this.machines.delete(id);
    this.changed();
  }

  override async pushKey(): Promise<string> {
    throw new CommandError("unavailable", "服务器没有响应");
  }
}
