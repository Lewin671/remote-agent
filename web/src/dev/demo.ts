// Development only: a Remote with made-up machines and sessions in every
// state docs/design/client-v8.html draws, and no server. Opened with ?demo
// on the dev server; builds leave this file out. Options:
//   ?demo&role=authority|member|machine&recovery=pending&pair=compare  management screens
//   ?demo&security=fail | archive=1 | public=missing  failure and archive screens
//   ?demo&conn=connecting|reconnecting|nonetwork|noserver|fail|removed   the server link's states
//   ?demo&term=history                       paged terminal history
//   ?demo&term=closed                        terminals that drop at once
//   ?demo&empty=1 | empty=offline | empty=none  accounts with no sessions
//   ?demo&list=fail                          the first listing fails
//   ?demo&prefs=fail                         saving project preferences fails
//   ?demo&long=1                            long project, machine and session names
//   ?demo&many=1&pick=1                     many sessions/projects, picker search
//   ?demo&project-settings=1#/p/<encoded path>   project settings sheet
//   ?demo&draft=1&sendmenu=1#/s/s-run       running draft and long-press menu
//   ?demo&queue-all=1#/s/s-multi           queue, held, paused, other device
//   ?demo&cancel=failed&queue-all=1#/s/s-multi  cancel retry
//   ?demo&deny=1                          inline optional denial reason
//   ?demo&longcmd=1#/s/s-appr               an approval of a 40-line command with a long description

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import type { Keys } from "../crypto";
import {
  Agent,
  ApprovalDecision,
  ApprovalMode,
  EventSchema,
  ImageFormat,
  ImageRefSchema,
  type ImageRef,
  ImageSource,
  ImageSync,
  InputStage,
  InputStatus,
  SendMode,
  OriginKind,
  SessionStatus,
  ToolKind,
  TurnOutcome,
  type Event,
} from "../gen/remoteagent/v1/event_pb";
import {
  DaemonSettingsSchema,
  ListModelsResponseSchema,
  SessionSchema,
  type Session,
} from "../gen/remoteagent/v1/local_pb";
import {
  CommandResultSchema,
  CommandSchema,
  MachineMetaSchema,
  type CommandResult,
  type TerminalFrame,
} from "../gen/remoteagent/v1/remote_pb";
import {
  CommandError,
  Remote,
  type CommandKind,
  type History,
  type RequestSlot,
  type SessionView,
  type TermConn,
} from "../remote";
import { applyOp, emptyPrefs, type PrefsSnapshot } from "../prefs";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import {
  DefaultModeSource,
  DeviceMetaSchema,
  QuotaState,
  type ProjectPrefs,
  ProjectPrefsSchema,
} from "../gen/remoteagent/v1/remote_pb";
import { PROTOCOL_VERSION } from "../protocol";
import scheduledFixture from "../../../testdata/schedules/clients.json";
import type { ScheduleTask } from "../schedules";

type EventKind = MessageInitShape<typeof EventSchema>["kind"];
type EventInit = MessageInitShape<typeof EventSchema>;

// This browser and two other clients of the demo account.
export const ME = "aaaa0000aaaa0000aaaa0000aaaaa8f2";
export const ANDROID = "bbbb0000bbbb0000bbbb0000bbbbb71c";
const BROWSER = "cccc0000cccc0000cccc0000cccc92de";
type SessionInit = MessageInitShape<typeof SessionSchema>;

const now = Date.now();
// A Claude Code write, approved from here: its row carries the result.
const writeInput = JSON.stringify({
  file_path: "/Users/me/Code/bar/errors.go",
  content: "package errors",
});
const min = 60_000;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const ctxAuto = {
  approvalsReviewer: "auto_review",
  approvalPolicy: "on-request",
  sandbox: "workspace-write",
  collaborationMode: "default",
};
const ctxAsk = {
  approvalsReviewer: "user",
  approvalPolicy: "on-request",
  sandbox: "workspace-write",
  collaborationMode: "default",
};
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
  // An event, or one with its origin: [kind, origin].
  events: (EventKind | [EventKind, EventInit["origin"]])[];
}

// Pictures: what each demo image looks like (drawn on a canvas in the
// browser, like the mockup's sketches), its size, and how GetImage treats
// it: exp- expired, fail- fails once, bad- is not what it claims.
type Look = "ui" | "empty" | "web" | "gen";
const looks = new Map<string, { look: Look; w: number; h: number }>();
let imageN = 0;
function img(
  look: Look,
  w: number,
  h: number,
  init: Omit<Partial<ImageRef>, "$typeName"> = {},
  id?: string,
): ImageRef {
  const imageId = id ?? `img${++imageN}`;
  looks.set(imageId, { look, w, h });
  const scale = 320 / Math.max(w, h);
  return create(ImageRefSchema, {
    imageId,
    source: ImageSource.USER,
    sync: ImageSync.SYNCED,
    format: ImageFormat.PNG,
    width: w,
    height: h,
    displayWidth: w,
    displayHeight: h,
    previewFormat: ImageFormat.PNG,
    previewWidth: Math.round(w * scale),
    previewHeight: Math.round(h * scale),
    ...init,
  });
}

/** Draws a demo picture; PNG bytes, or undefined without a canvas. */
async function draw(
  look: Look,
  w: number,
  h: number,
): Promise<Uint8Array | undefined> {
  if (typeof OffscreenCanvas === "undefined") return undefined;
  const c = new OffscreenCanvas(w, h);
  const g = c.getContext("2d");
  if (!g) return undefined;
  const rect = (
    x: number,
    y: number,
    rw: number,
    rh: number,
    color: string,
  ) => {
    g.fillStyle = color;
    g.fillRect(x * w, y * h, rw * w, rh * h);
  };
  if (look === "gen") {
    const sky = g.createLinearGradient(0, 0, 0, h);
    sky.addColorStop(0, "#8ec5ff");
    sky.addColorStop(0.72, "#e4f2ff");
    g.fillStyle = sky;
    g.fillRect(0, 0, w, h);
    g.fillStyle = "#ffd27a";
    g.beginPath();
    g.arc(w * 0.76, h * 0.28, Math.min(w, h) * 0.09, 0, 7);
    g.fill();
    g.fillStyle = "#4c9a74";
    g.beginPath();
    g.ellipse(w * 0.28, h, w * 0.6, h * 0.4, 0, 0, 7);
    g.fill();
    g.fillStyle = "#2f7a5a";
    g.beginPath();
    g.ellipse(w * 0.84, h * 1.04, w * 0.7, h * 0.46, 0, 0, 7);
    g.fill();
  } else {
    rect(0, 0, 1, 1, look === "web" ? "#ffffff" : "#fafbfc");
    rect(
      0,
      0,
      1,
      look === "web" ? 0.1 : 0.12,
      look === "web" ? "#e3e7ec" : "#1f2733",
    );
    if (look === "ui") {
      rect(0.15, 0.31, 0.56, 0.05, "#d5dae1");
      rect(0.15, 0.43, 0.44, 0.05, "#d5dae1");
      rect(0.1, 0.56, 0.8, 0.16, "#e7eaee");
      rect(0.15, 0.84, 0.7, 0.07, "#2f6feb");
    } else if (look === "empty") {
      g.fillStyle = "#c9d6f2";
      g.beginPath();
      g.arc(w * 0.5, h * 0.4, Math.min(w, h) * 0.13, 0, 7);
      g.fill();
      rect(0.22, 0.55, 0.56, 0.04, "#d5dae1");
      rect(0.31, 0.62, 0.38, 0.03, "#e3e6ea");
    } else {
      rect(0, 0.1, 0.24, 0.9, "#eef1f4");
      rect(0.4, 0.27, 0.46, 0.07, "#d5dae1");
      rect(0.4, 0.43, 0.34, 0.07, "#d5dae1");
      rect(0.74, 0.7, 0.16, 0.09, "#2f6feb");
    }
  }
  const blob = await c.convertToBlob({ type: "image/png" });
  return new Uint8Array(await blob.arrayBuffer());
}

const fromMe = (requestId = "") => ({
  kind: OriginKind.CLIENT,
  deviceId: ME,
  requestId,
});
const fromAndroid = { kind: OriginKind.CLIENT, deviceId: ANDROID };
const fromBrowser = { kind: OriginKind.CLIENT, deviceId: BROWSER };
const fromLocal = { kind: OriginKind.LOCAL };

function fixtures(): Record<string, Fixture> {
  return {
    "s-run": {
      machine: "mac",
      updated: now - 20_000,
      info: {
        title: "修复登录超时",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.RUNNING,
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAuto,
        contextTokens: 87_000n,
        contextWindow: 760_000n,
        preview: "正在执行 go test ./auth/...",
      },
      events: [
        {
          case: "userMessage",
          value: { text: "登录接口偶尔超时，帮我查一下原因并修掉" },
        },
        { case: "turnStarted", value: {} },
        {
          case: "reasoning",
          value: { text: "先看登录的实现，再看连接池的配置。" },
        },
        {
          case: "toolCall",
          value: {
            title: "rg -n Login auth",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
            output: "auth/login.go:56:func Login(",
          },
        },
        {
          case: "toolCall",
          value: {
            title: "sed -n 40,80p auth/login.go",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        {
          case: "toolCall",
          value: {
            title:
              "cat /Users/me/Code/foo/auth/redis.go /Users/me/.config/redis.conf",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        { case: "agentMessage", value: { text: answer } },
        {
          case: "toolCall",
          value: {
            title: "/Users/me/Code/foo/auth/login.go",
            kind: ToolKind.FILE_CHANGE,
            status: "completed",
          },
        },
        {
          case: "toolStarted",
          value: { title: "go test /Users/me/Code/foo/auth/..." },
        },
        {
          case: "inputState",
          value: {
            requestId: "q-run",
            status: InputStatus.QUEUED,
            text: "顺便补一个测试",
            mode: SendMode.STEER,
          },
        },
      ],
    },
    "s-appr": {
      machine: "vps",
      updated: now - 10 * min,
      info: {
        title: "重构 API 层",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.RUNNING,
        pendingApprovals: 2,
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAsk,
        contextTokens: 23n,
        contextWindow: 100n,
      },
      events: [
        { case: "userMessage", value: { text: "把 handler 拆到单独的包里" } },
        { case: "turnStarted", value: {} },
        {
          case: "toolCall",
          value: {
            title: "/Users/me/Code/foo/internal/handler/api.go",
            kind: ToolKind.FILE_CHANGE,
            status: "completed",
          },
        },
        {
          case: "approvalRequested",
          value: {
            approvalId: "a0",
            command: "mkdir -p internal/handler",
            toolName: "shell",
          },
        },
        {
          case: "approvalResolved",
          value: {
            approvalId: "a0",
            decision: ApprovalDecision.ALLOW_ALWAYS,
            message:
              "Yes, and don't ask again for commands that start with `mkdir`",
          },
        },
        {
          case: "agentMessage",
          value: { text: "拆分完成，跑一下测试确认没有破坏现有行为。" },
        },
        {
          case: "approvalRequested",
          value: {
            approvalId: "a1",
            toolName: "shell",
            description:
              "跑一遍全部测试，确认拆包没有破坏行为。先清掉旧的生成文件再用 buf 重新生成，编译通过后只跑配对、会话和终端三组测试，最后看一眼 gen 目录有没有意外的改动；任何一步失败都会停下来，不会继续执行后面的命令。",
            command:
              "rm -rf gen/remoteagent && \\\nbuf generate --template buf.gen.yaml && \\\ngo build ./... && \\\ngo test ./internal/... -count=1 -run 'Test(Pair|Session|Terminal)' && \\\ngit status --short gen/",
            deadline: timestampFromMs(now + 8 * min),
          },
        },
        {
          case: "approvalRequested",
          value: {
            approvalId: "a2",
            toolName: "apply_patch",
            description: "修改工作区外的文件",
            toolInputJson: '{"path": "/etc/hosts", "patch": "@@ -1,3 +1,4 @@"}',
            deadline: timestampFromMs(now + 6 * min),
          },
        },
      ],
    },
    "s-ask": {
      machine: "imac",
      updated: now - 21 * min,
      info: {
        title: "升级依赖",
        cwd: "/Users/me/Code/bar",
        status: SessionStatus.RUNNING,
        pendingQuestions: 1,
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAuto,
      },
      events: [
        { case: "userMessage", value: { text: "把依赖都升级到最新的版本" } },
        { case: "turnStarted", value: {} },
        {
          case: "toolCall",
          value: {
            title: "go list -m -u all",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        {
          case: "agentMessage",
          value: {
            text: "有 12 个依赖可以升级，其中 `pgx` 是 v4 → v5 的大版本。",
          },
        },
        {
          case: "questionAsked",
          value: {
            questionId: "q1",
            questions: [
              {
                text: "大版本也一起升吗？",
                options: [
                  {
                    label: "只升小版本 (Recommended)",
                    description: "改动小，go build 大概率直接通过",
                  },
                  {
                    label: "包括 pgx v5",
                    description: "要改连接池和事务相关的调用",
                  },
                ],
              },
              {
                text: "要顺便更新 go.mod 的 Go 版本吗？",
                options: [{ label: "更新到 1.27" }, { label: "保持不变" }],
              },
            ],
          },
        },
        {
          case: "inputState",
          value: { requestId: "q-ask", status: InputStatus.QUEUED },
        },
      ],
    },
    "s-claude": {
      machine: "mac",
      updated: now - 30 * min,
      info: {
        title: "给登录接口加限流",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.RUNNING,
        pendingQuestions: 1,
        agent: Agent.CLAUDE,
        model: "claude-sonnet-5-5",
        effort: "high",
        mode: ApprovalMode.ASK,
        permissionMode: "default",
      },
      events: [
        { case: "userMessage", value: { text: "给登录接口加限流" } },
        { case: "turnStarted", value: {} },
        {
          case: "toolStarted",
          value: {
            title: "Read /Users/me/Code/foo/auth/login.go",
            callId: "t1",
          },
        },
        {
          case: "toolStarted",
          value: { title: "Read auth/limit.go", callId: "t2" },
        },
        {
          case: "toolStarted",
          value: { title: "Grep RateLimit", callId: "t3" },
        },
        {
          case: "toolCall",
          value: {
            title: "Read auth/limit.go",
            callId: "t2",
            kind: ToolKind.OTHER,
            status: "completed",
          },
        },
        {
          case: "approvalRequested",
          value: { approvalId: "ct", command: "npm test", toolName: "Bash" },
        },
        {
          case: "approvalResolved",
          value: { approvalId: "ct", decision: ApprovalDecision.TERMINAL },
        },
        {
          case: "questionAsked",
          value: {
            questionId: "cq",
            questions: [
              {
                id: "限流按哪些维度？",
                text: "限流按哪些维度？",
                multiSelect: true,
                options: [
                  { label: "按 IP" },
                  { label: "按账户", description: "登录后才有" },
                  { label: "按设备" },
                ],
              },
            ],
          },
        },
      ],
    },
    "s-claude-idle": {
      machine: "mac",
      updated: now - 35 * min,
      info: {
        title: "整理错误码",
        cwd: "/Users/me/Code/bar",
        status: SessionStatus.IDLE,
        agent: Agent.CLAUDE,
        model: "claude-haiku-4-5-20251001",
        permissionMode: "acceptEdits",
        fullAccessUnavailable: true,
        preview: "错误码已按模块分组",
      },
      events: [
        { case: "userMessage", value: { text: "整理错误码" } },
        {
          case: "approvalRequested",
          value: {
            approvalId: "cw",
            toolName: "Write",
            toolInputJson: writeInput,
            inputDigest: "demo-write",
          },
        },
        {
          case: "approvalResolved",
          value: { approvalId: "cw", decision: ApprovalDecision.ALLOW },
        },
        {
          case: "toolCall",
          value: {
            title: "/Users/me/Code/bar/errors.go",
            itemType: "Write",
            kind: ToolKind.FILE_CHANGE,
            status: "completed",
            inputDigest: "demo-write",
          },
        },
        { case: "agentMessage", value: { text: "错误码已按模块分组。" } },
      ],
    },
    "s-plan": {
      machine: "imac",
      updated: now - 40 * min,
      info: {
        title: "拆分订单服务",
        cwd: "/Users/me/Code/shop",
        status: SessionStatus.IDLE,
        planOpen: true,
        model: "gpt-6-sol",
        effort: "high",
        ...ctxPlan,
      },
      events: [
        {
          case: "turnContext",
          value: { ...ctxAsk, model: "gpt-6-sol", effort: "high" },
        },
        {
          case: "turnContext",
          value: { ...ctxPlan, model: "gpt-6-sol", effort: "high" },
        },
        {
          case: "userMessage",
          value: { text: "先出个方案，把订单服务里的库存逻辑拆出去" },
        },
        {
          case: "toolCall",
          value: {
            title: "读取 14 个文件",
            kind: ToolKind.OTHER,
            status: "completed",
          },
        },
        { case: "agentMessage", value: { text: "方案如下，批准后开始改。" } },
        {
          case: "planProposed",
          value: {
            text: "1. 新建 `inventory` 包，迁移库存扣减和回滚\n2. 订单服务改为调用接口，保留旧实现做开关\n3. 补充集成测试，覆盖并发扣减\n4. 灰度一周后删掉旧实现\n5. 更新文档和监控面板",
          },
        },
      ],
    },
    "s-popup": {
      machine: "imac",
      updated: now - 50 * min,
      info: {
        title: "",
        cwd: "/Users/me/Code/billing",
        status: SessionStatus.BLOCKED,
        statusDetail: "Trust this folder?",
        createdAt: timestampFromMs(new Date().setHours(11, 25, 0, 0)),
        model: "gpt-6-astra",
        effort: "medium",
      },
      events: [
        {
          case: "inputState",
          value: { requestId: "q-popup", status: InputStatus.QUEUED },
        },
      ],
    },
    "s-idle": {
      machine: "mac",
      updated: now - 90 * min,
      info: {
        title: "补充接口文档",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.IDLE,
        preview: "已补充 12 个接口的说明，要生成 OpenAPI 吗？",
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAsk,
      },
      events: [
        { case: "userMessage", value: { text: "补充接口文档" } },
        {
          case: "agentMessage",
          value: { text: "已补充 12 个接口的说明，要生成 OpenAPI 吗？" },
        },
      ],
    },
    "s-fresh": {
      machine: "imac",
      updated: now - 100 * min,
      info: {
        title: "",
        cwd: "/Users/me/Code/billing",
        status: SessionStatus.IDLE,
        createdAt: timestampFromMs(new Date().setHours(11, 25, 0, 0)),
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAsk,
      },
      events: [],
    },
    "s-ctx": {
      machine: "mac",
      updated: now - 120 * min,
      info: {
        title: "重写支付模块",
        cwd: "/Users/me/Code/shop",
        status: SessionStatus.IDLE,
        model: "gpt-6-sol",
        effort: "high",
        ...ctxAuto,
        contextTokens: 86n,
        contextWindow: 100n,
      },
      events: [
        {
          case: "tokenUsage",
          value: { lastTotalTokens: 91n, contextWindow: 100n },
        },
        { case: "settingsChanged", value: { compacted: true } },
        {
          case: "tokenUsage",
          value: { lastTotalTokens: 14n, contextWindow: 100n },
        },
        { case: "turnStarted", value: {} },
        {
          case: "toolCall",
          value: {
            title: "编辑 9 个文件  +412 −288",
            kind: ToolKind.OTHER,
            status: "completed",
          },
        },
        {
          case: "agentMessage",
          value: { text: "支付回调和退款流程都已迁移到新接口，测试全部通过。" },
        },
        {
          case: "turnEnded",
          value: { outcome: TurnOutcome.COMPLETED, durationMs: 252_000n },
        },
        { case: "userMessage", value: { text: "继续把对账逻辑也迁过去" } },
      ],
    },
    "s-msg": {
      machine: "mac",
      updated: now - 150 * min,
      info: {
        title: "消息的各种状态",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.IDLE,
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAsk,
      },
      events: [
        { case: "agentMessage", value: { text: long } },
        {
          case: "toolCall",
          value: {
            title: "go build ./...",
            kind: ToolKind.COMMAND,
            status: "failed",
            exitCode: 1,
            output:
              "./auth/login.go:58:9: undefined: pool\n" +
              Array.from(
                { length: 12 },
                (_, i) => `./auth/x.go:${i}: error`,
              ).join("\n"),
          },
        },
        {
          case: "inputState",
          value: { requestId: "o-unconf", status: InputStatus.UNCONFIRMED },
        },
        {
          case: "approvalRequested",
          value: { approvalId: "r1", command: "rm -rf build/" },
        },
        {
          case: "approvalResolved",
          value: { approvalId: "r1", decision: ApprovalDecision.TIMEOUT },
        },
        {
          case: "approvalRequested",
          value: {
            approvalId: "r2",
            command: "curl https://example.com/install.sh",
          },
        },
        {
          case: "approvalResolved",
          value: { approvalId: "r2", decision: ApprovalDecision.DENY },
        },
        {
          case: "approvalRequested",
          value: { approvalId: "r3", command: "npm install" },
        },
        {
          case: "approvalResolved",
          value: { approvalId: "r3", decision: ApprovalDecision.CANCELED },
        },
        {
          case: "inputState",
          value: {
            requestId: "o-failed",
            status: InputStatus.QUEUED,
            text: "这两个页面底部的按钮都点不到",
          },
        },
        {
          case: "inputState",
          value: {
            requestId: "o-failed",
            status: InputStatus.FAILED,
            reason: "image_not_recognized",
          },
        },
        {
          case: "inputState",
          value: {
            requestId: "o-term",
            status: InputStatus.QUEUED,
            text: "对比一下这两张截图",
          },
        },
        {
          case: "inputState",
          value: {
            requestId: "o-term",
            status: InputStatus.NEEDS_TERMINAL,
            reason: "input_changed",
          },
        },
      ],
    },
    "s-multi": {
      machine: "vps",
      updated: now - 12 * min,
      info: {
        title: "重构订单模块",
        cwd: "/Users/me/Code/shop",
        status: SessionStatus.RUNNING,
        agent: Agent.CLAUDE,
        model: "claude-sonnet-5-5",
        effort: "high",
        mode: ApprovalMode.PLAN,
        permissionMode: "plan",
        contextTokens: 23n,
        contextWindow: 100n,
      },
      events: [
        [
          {
            case: "userMessage",
            value: { text: "把库存回滚的测试补上", requestId: "m-me" },
          },
          fromMe("m-me"),
        ],
        [
          { case: "userMessage", value: { text: "不要修改公开接口" } },
          fromAndroid,
        ],
        [
          { case: "userMessage", value: { text: "先整理一下实现方案" } },
          fromLocal,
        ],
        [
          { case: "settingsChanged", value: { mode: ApprovalMode.PLAN } },
          fromBrowser,
        ],
        {
          case: "approvalRequested",
          value: {
            approvalId: "mx",
            command: "go test ./...",
            toolName: "Bash",
          },
        },
        [
          {
            case: "approvalResolved",
            value: { approvalId: "mx", decision: ApprovalDecision.ALLOW },
          },
          fromAndroid,
        ],
        { case: "turnStarted", value: {} },
        [
          {
            case: "inputState",
            value: {
              requestId: "q-multi",
              status: InputStatus.QUEUED,
              stage: InputStage.WAITING,
              mode: SendMode.QUEUE,
              text: "补充库存回滚测试。",
            },
          },
          fromMe(),
        ],
        [
          {
            case: "inputState",
            value: {
              requestId: "q-android",
              status: InputStatus.QUEUED,
              stage: InputStage.WAITING,
              mode: SendMode.QUEUE,
              text: "最后检查 README。",
            },
          },
          fromAndroid,
        ],
        [
          {
            case: "inputState",
            value: {
              requestId: "q-held",
              status: InputStatus.QUEUED,
              mode: SendMode.STEER,
              text: "这条消息会在当前工具结束后并入。",
            },
          },
          fromMe(),
        ],
        [
          {
            case: "inputState",
            value: { requestId: "q-held", status: InputStatus.HELD_BY_CLI },
          },
          fromMe(),
        ],
        [
          {
            case: "inputState",
            value: {
              requestId: "q-paused",
              status: InputStatus.QUEUED,
              mode: SendMode.QUEUE,
              paused: true,
              text: "中断前排着的这条消息需要手动发送。",
            },
          },
          fromMe(),
        ],
      ],
    },
    "s-img": {
      machine: "mac",
      updated: now - 14 * min,
      info: {
        title: "调整空状态",
        cwd: "/Users/me/Code/web",
        status: SessionStatus.IDLE,
        model: "gpt-6-astra",
        effort: "medium",
        ...ctxAuto,
        contextTokens: 18n,
        contextWindow: 100n,
      },
      events: [
        [
          {
            case: "userMessage",
            value: {
              text: "[Image #1] 参考这张改一下空状态",
              images: [
                img("empty", 1170, 2532, {
                  cliIndex: 1,
                  path: "/Users/me/Desktop/empty-before.png",
                }),
              ],
            },
          },
          fromLocal,
        ],
        { case: "turnStarted", value: {} },
        {
          case: "toolCall",
          value: {
            title: "rg -n EmptyState web/src",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        {
          case: "toolCall",
          value: {
            title: "pnpm dev",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        {
          case: "toolCall",
          value: {
            title: "view_image shots/empty-before.png",
            kind: ToolKind.OTHER,
            status: "completed",
            images: [
              img("empty", 1170, 2532, {
                source: ImageSource.TOOL,
                path: "/Users/me/Code/web/shots/empty-before.png",
              }),
            ],
          },
        },
        {
          case: "toolCall",
          value: {
            title: "view_image shots/empty-after.png",
            kind: ToolKind.OTHER,
            status: "completed",
            images: [
              img("ui", 1170, 2532, {
                source: ImageSource.TOOL,
                path: "/Users/me/Code/web/shots/empty-after.png",
              }),
            ],
          },
        },
        {
          case: "agentMessage",
          value: { text: "改好了，左边是改前，右边是改后。" },
        },
        {
          case: "turnEnded",
          value: { outcome: TurnOutcome.COMPLETED, durationMs: 64_000n },
        },
        {
          case: "userMessage",
          value: { text: "生成一张空状态插画，风格和现在的图标一致" },
        },
        {
          case: "imageGenerated",
          value: {
            callId: "ig1",
            revisedPrompt:
              "A flat, friendly illustration of an empty inbox on a hill under a warm sun, soft blue sky, matching a rounded icon set.",
            images: [
              img("gen", 1536, 1024, {
                source: ImageSource.GENERATED,
                path: "/Users/me/Code/web/public/empty.png",
              }),
            ],
          },
        },
        {
          case: "agentMessage",
          value: {
            text: "生成好了，放到了 [web/public/empty.png](/Users/me/Code/web/public/empty.png)。",
          },
        },
      ],
    },
    "s-img-claude": {
      machine: "mac",
      updated: now - 16 * min,
      info: {
        title: "首页改版",
        cwd: "/Users/me/Code/web",
        status: SessionStatus.IDLE,
        agent: Agent.CLAUDE,
        model: "claude-sonnet-5-5",
        effort: "medium",
        mode: ApprovalMode.ASK,
        permissionMode: "default",
        contextTokens: 24n,
        contextWindow: 100n,
      },
      events: [
        [
          {
            case: "userMessage",
            value: {
              text: "[Image #1]电脑上，[Image #2]手机上，为什么手机上这么挤？",
              images: [
                img("web", 2880, 1800, { cliIndex: 1 }),
                img("ui", 1170, 2532, { cliIndex: 2 }),
              ],
            },
          },
          fromLocal,
        ],
        {
          case: "toolCall",
          value: {
            title: "Read shots/home-mobile-v1.png",
            itemType: "Read",
            kind: ToolKind.OTHER,
            status: "completed",
            images: [
              img("ui", 1170, 2532, {
                source: ImageSource.TOOL,
                path: "/Users/me/Code/web/shots/home-mobile-v1.png",
                originalWidth: 1170,
                originalHeight: 2532,
              }),
            ],
          },
        },
        {
          case: "agentMessage",
          value: { text: "窄屏下还是两列、间距 8px。我改成单列、间距 16px。" },
        },
        {
          case: "toolCall",
          value: {
            title: "Read shots/*.png",
            itemType: "Read",
            kind: ToolKind.OTHER,
            status: "completed",
            images: [
              img("web", 2880, 1800, {
                source: ImageSource.TOOL,
                sync: ImageSync.NAME_ONLY,
                path: "/Users/me/Code/web/shots/full-page.png",
                previewFormat: ImageFormat.UNSPECIFIED,
              }),
              img("web", 2880, 1800, {
                source: ImageSource.TOOL,
                sync: ImageSync.PREVIEW_ONLY,
                path: "/Users/me/Code/web/shots/wide.png",
              }),
              img(
                "web",
                2880,
                1800,
                {
                  source: ImageSource.TOOL,
                  path: "/Users/me/Code/web/shots/old.png",
                },
                "exp-1",
              ),
              img(
                "web",
                2880,
                1800,
                {
                  source: ImageSource.TOOL,
                  path: "/Users/me/Code/web/shots/flaky.png",
                },
                "fail-1",
              ),
            ],
          },
        },
        [
          {
            case: "userMessage",
            value: {
              text: "这几张都看一下",
              images: [
                img("ui", 1170, 2532, { cliIndex: 3 }),
                img("empty", 1170, 2532, { cliIndex: 4 }),
                img("ui", 1170, 2532, { cliIndex: 5 }),
                img("empty", 1170, 2532, { cliIndex: 6 }),
                img("web", 1600, 1000, { cliIndex: 7 }),
                img("web", 1600, 1000, { cliIndex: 8 }),
                img("ui", 1170, 2532, { cliIndex: 9 }, "bad-1"),
              ],
            },
          },
          fromMe(),
        ],
        {
          case: "agentMessage",
          value: { text: "单列布局改好了，测试也通过了。" },
        },
      ],
    },
    "s-hist": {
      machine: "mac",
      updated: now - 3 * 60 * min,
      info: {
        title: "历史读取失败",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.IDLE,
        model: "gpt-6-astra",
        effort: "medium",
      },
      events: [{ case: "userMessage", value: { text: "这之后的记录读不到" } }],
    },
    "s-ended": {
      machine: "mac",
      updated: now - 2 * 24 * 60 * min,
      info: {
        title: "介绍下这个项目",
        cwd: "/Users/me/Code/ai-bot/remote-agent",
        status: SessionStatus.EXITED,
        preview: "没有按时间失效。模型列表缓存在前端页面的内存里…",
        model: "gpt-6-astra",
        effort: "medium",
      },
      events: [
        { case: "userMessage", value: { text: "缓存是多久失效" } },
        {
          case: "agentMessage",
          value: {
            text: "没有按时间失效。模型列表缓存在前端页面的内存里，**同一台机器每次页面加载只请求一次**；刷新或重新打开页面会重新获取。请求失败时会立即清除缓存（[web/src/remote.ts:612](/Users/me/Code/ai-bot/remote-agent/web/src/remote.ts:612)），下次调用会重试。",
          },
        },
        {
          case: "sessionState",
          value: { status: SessionStatus.EXITED, detail: "tmux session gone" },
        },
      ],
    },
    "s-ended2": {
      machine: "mac",
      updated: now - 26 * 60 * min,
      info: {
        title: "补充 README",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.EXITED,
        preview: "已补充安装步骤",
      },
      events: [],
    },
    "s-ended3": {
      machine: "vps",
      updated: now - 3 * 24 * 60 * min,
      info: {
        title: "升级 CI",
        cwd: "/Users/me/Code/foo",
        status: SessionStatus.EXITED,
        preview: "CI 改用 Go 1.27",
      },
      events: [],
    },
    "s-off": {
      machine: "mini",
      updated: now - 2 * 60 * min,
      info: {
        title: "对账脚本",
        cwd: "/Users/me/Code/billing",
        status: SessionStatus.IDLE,
        preview: "已导出到 out/2026-03.csv，共 1,284 行。",
        model: "gpt-6-astra",
        effort: "medium",
      },
      events: [
        { case: "userMessage", value: { text: "把 3 月的对账结果导出成 CSV" } },
        {
          case: "toolCall",
          value: {
            title: "python scripts/export.py --month 2026-03",
            kind: ToolKind.COMMAND,
            status: "completed",
            exitCode: 0,
          },
        },
        {
          case: "agentMessage",
          value: {
            text: "已导出到 [out/2026-03.csv](/Users/me/Code/billing/out/2026-03.csv)，共 1,284 行。",
          },
        },
      ],
    },
  };
}

const models = create(ListModelsResponseSchema, {
  defaultModel: "gpt-6-astra",
  models: [
    {
      slug: "gpt-6-astra",
      displayName: "GPT-6-Astra",
      efforts: ["low", "medium", "high", "xhigh", "max", "ultra"],
      defaultEffort: "medium",
    },
    {
      slug: "gpt-6-sol",
      displayName: "GPT-6-Sol",
      efforts: ["low", "medium", "high", "xhigh", "max", "ultra"],
      defaultEffort: "high",
    },
    {
      slug: "gpt-6-luna",
      displayName: "GPT-6-Luna",
      efforts: ["low", "medium", "high", "xhigh", "max"],
      defaultEffort: "low",
    },
    {
      slug: "gpt-6-nova",
      displayName: "GPT-6-Nova",
      efforts: ["low", "medium"],
      defaultEffort: "low",
    },
  ],
});
const claudeModels = create(ListModelsResponseSchema, {
  defaultModel: "claude-opus-5-5",
  models: [
    {
      slug: "claude-opus-5-5",
      displayName: "Opus 5.5",
      efforts: ["low", "medium", "high", "xhigh", "max"],
      defaultEffort: "medium",
      contextWindow: 1_000_000n,
    },
    {
      slug: "claude-fable-5-1",
      displayName: "Fable 5.1",
      efforts: ["low", "medium", "high", "xhigh", "max"],
      defaultEffort: "high",
      contextWindow: 1_000_000n,
    },
    {
      slug: "claude-sonnet-5-5",
      displayName: "Sonnet 5.5",
      efforts: ["low", "medium", "high", "xhigh", "max"],
      defaultEffort: "medium",
      contextWindow: 1_000_000n,
    },
    {
      slug: "claude-haiku-5-5",
      displayName: "Haiku 5.5",
      efforts: ["low", "medium", "high", "xhigh", "max"],
      defaultEffort: "medium",
      contextWindow: 1_000_000n,
    },
    {
      slug: "claude-haiku-4-5-20251001",
      displayName: "Haiku 4.5",
      contextWindow: 200_000n,
      autoUnavailable: true,
    },
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
    '    57 \x1b[33m-       conn, err := redis.Dial("tcp", cfg.RedisAddr)\x1b[0m',
    "    58 \x1b[32m+       conn := pool.Get()\x1b[0m",
    "",
    `\x1b[2m• Running\x1b[0m ${title}`,
    "",
    "\x1b[34m◦ Working (12s • esc to interrupt)\x1b[0m",
    "",
    "› ",
  ].join("\r\n");

const trust = [
  "\x1b[33m  Folder access\x1b[0m",
  "  ~/Code/billing",
  "",
  "  Trust this folder? Codex can",
  "  read, edit, and run files here.",
  "",
  "\x1b[34m› 1. Trust and continue\x1b[0m",
  "  2. Quit",
  "",
  "  enter continue · esc quit",
].join("\r\n");

// One coloured line of ?demo&term=history: a diff's red and green, bold, a
// 256-colour line, one mixing CJK with ASCII, and one wider than a phone.
const historyLine = (n: number) => {
  const tag = `history-${String(n).padStart(4, "0")}`;
  switch (n % 20) {
    case 0:
      return `\x1b[2m• Explored\x1b[0m ${tag}`;
    case 5:
      return `  \x1b[33m-  old ${tag}\x1b[0m`;
    case 6:
      return `  \x1b[32m+  new ${tag}\x1b[0m`;
    case 10:
      return `\x1b[1m${tag}\x1b[0m 加粗的中文和 ASCII`;
    case 15:
      return `\x1b[38;5;208m${tag} 256 色\x1b[0m`;
    case 18:
      return `\x1b[4m${tag}\x1b[0m 下划线 \x1b[7m反显\x1b[0m`;
    case 3:
      return `${tag} ${"wide-line ".repeat(11)}| 第 120 列附近`;
    default:
      return `${tag} 中文 mixed text`;
  }
};

/** A migration script of 40 lines, some of them long enough to wrap. */
const longCommand = [
  "set -euo pipefail",
  ...Array.from({ length: 36 }, (_, i) =>
    i % 6 === 5
      ? `psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c "ALTER TABLE orders_${i} ADD COLUMN IF NOT EXISTS inventory_reserved_at timestamptz"`
      : `echo "step ${i + 1}: migrating shard ${i}" && ./bin/migrate --shard ${i} --dry-run=false`,
  ),
  "./bin/check-consistency --all",
  "echo done",
  "git status --short",
].join("\n");

export class DemoRemote extends Remote {
  // These views are locally authored samples, with no relay or account data.
  override currentDerived(_sid: string, d: import("../model").Derived) {
    return d;
  }
  override async refreshLiveSnapshot(_sid: string): Promise<void> {}
  override demoFlag(name: string): boolean {
    return this.opts.has(name);
  }
  private evs = new Map<string, Event[]>();
  private attempts = new Map<string, number>();
  // UI tests keep the demo behavior but skip simulated network latency.
  private pause = sleep;

  static create(q: URLSearchParams, pause = sleep): DemoRemote {
    const r = new DemoRemote(
      {
        server: "https://ra.example.com",
        root: {} as CryptoKey,
        rootRaw: new Uint8Array(32),
        devicePrivate: {} as CryptoKey,
        deviceId: ME,
      },
      {} as Keys,
    );
    r.opts = q;
    r.pause = pause;
    // Prepared as for real, remembering the file's name so that one named
    // with "fail" can fail its upload.
    const real = r.prepareImage;
    r.prepareImage = async (f, original) => {
      let p;
      try {
        p = await real(f, original);
      } catch {
        p = {
          bytes: new Uint8Array(await f.arrayBuffer()),
          format: "png" as const,
          width: 1,
          height: 1,
        };
      }
      return Object.assign(p, { name: (f as File).name ?? "" });
    };
    r.seed();
    r.seedAuthority();
    if (q.has("tables"))
      r.evs.set("s-run", [
        create(EventSchema, {
          seq: 1n,
          sessionId: "s-run",
          kind: {
            case: "agentMessage",
            value: {
              text: [
                "表格布局测试（示例数值）",
                "",
                "| 估值口径 | 每股收益 EPS | 市盈率 |",
                "| --- | ---: | ---: |",
                "| 2026 财年实际，GAAP | $74.33 | **14.5 倍** |",
                "| 2026 财年实际，调整后 | $75.52 | **14.3 倍** |",
                "| 下季指引中值 ×4 | $152.60 | **7.1 倍** |",
                "| 第三方列示的 2027 财年预期 | $159.03 | **6.8 倍** |",
                "",
                "| 全年调整后 EPS | 比约 $160 的预期低 | 当前价格对应 PE |",
                "| ---: | ---: | ---: |",
                "| $120 | 25% | **9.0 倍** |",
                "| $100 | 37.5% | **10.8 倍** |",
                "| $80 | 50% | **13.5 倍** |",
                "",
                "| 长内容 | 空列 | 链接 |",
                "| --- | --- | --- |",
                "| 很长的内容" +
                  "不会把整张表无限撑宽".repeat(8) +
                  " | | [文档](https://example.com) |",
                "",
                "| 第一列 | 第二列 | 第三列 | 第四列 | 第五列 | 第六列 |",
                "| --- | --- | --- | --- | --- | --- |",
                "| 很长的单元格一 | 很长的单元格二 | 很长的单元格三 | 很长的单元格四 | 很长的单元格五 | 很长的单元格六 |",
              ].join("\n"),
            },
          },
        }),
      ]);
    const history = Math.min(
      50_000,
      Math.max(0, Number(q.get("history")) || 0),
    );
    if (history)
      r.evs.set(
        "s-run",
        Array.from({ length: history }, (_, i) =>
          create(EventSchema, {
            seq: BigInt(i + 1),
            sessionId: "s-run",
            kind: {
              case: "agentMessage",
              value: {
                text:
                  `性能示例 ${i + 1}\n\n` +
                  "一段带有 **强调** 和 `代码` 的消息。\n".repeat((i % 5) + 1),
              },
            },
          }),
        ),
      );
    if (q.has("scheduled-final")) r.seedSchedules();
    return r;
  }

  private seedSchedules() {
    const f = scheduledFixture, machine = this.machines.get("mac")!;
    machine.meta!.hostname = f.machineName;
    machine.meta!.capabilities = { ...machine.meta!.capabilities!, scheduledTasks: true };
    this.machines.clear(); this.machines.set("mac", machine);
    const view = this.sessions.get("s-idle")!;
    view.id = f.session; view.machineId = "mac";
    view.info = create(SessionSchema, { id:f.session, title:f.sessionTitle, cwd:"/example/project", agent:Agent.CLAUDE, status:SessionStatus.IDLE, mode:ApprovalMode.AUTO });
    this.sessions.clear(); this.sessions.set(f.session, view);
    this.evs.clear();
    this.evs.set(f.session, [create(EventSchema, {seq:1n,sessionId:f.session,kind:{case:"userMessage",value:{text:"检查 PR 的回复。"}}}),create(EventSchema, {seq:2n,sessionId:f.session,kind:{case:"agentMessage",value:{text:"有变化时会通知你。"}}})]);
    this.schedules.set("mac", {revision:1n,tasks:f.tasks as ScheduleTask[],loading:false,error:""});
    this.stored={revision:1n,prefs:create(ProjectPrefsSchema,{schema:1,settings:[{path:"/example/project",machineId:"mac",agent:Agent.CLAUDE,agents:[{agent:Agent.CLAUDE,model:"haiku",mode:ApprovalMode.AUTO}]}]})};
  }

  private opts = new URLSearchParams();

  private removedDemoDevices = new Set<string>();
  private seedAuthority() {
    this.authorityDeviceId = this.opts.get("role") === "authority" ? ME : ANDROID;
    this.security.recoveryPending = this.opts.get("recovery") === "pending";
    this.security.recoveryReadyMs = BigInt(Date.now() + 47 * 60 * min);
    this.security.recoveryStatusStale = false;
    const hash = new Uint8Array(32);
    const record = { stateHash: hash, lease: { head: hash, notAfter: BigInt(Date.now() + 48 * 60 * min) } };
    this.security.verifiedRecord = async () => record as Awaited<ReturnType<typeof this.security.verifiedRecord>>;
    let pending: import("../security-operation").SecurityOperation | undefined;
    const work = async () => { await this.pause(200); if (this.opts.get("security") === "fail") throw new Error("没能保存安全操作，请重试"); };
    this.authority.pending = async () => pending;
    this.authority.hasAccountPublicProfile = async () => this.opts.get("public") !== "missing";
    this.authority.needsRecoveryCodeRotation = async () => this.opts.has("rotation");
    this.authority.renewLeaseNow = async () => { await work(); record.lease.notAfter = BigInt(Date.now() + 72 * 60 * min); };
    const prepare = async () => {
      await work();
      return { code: "RA3 DEMO-ONLY-OFFLINE-BACKUP EXAMPLE-NOT-A-RECOVERY-CODE", storagePersisted: true };
    };
    this.authority.prepareRecovery = () => prepare();
    this.authority.prepareHandoffCode = () => prepare();
    this.authority.publishRecovery = async () => { await work(); pending = undefined; this.security.recoveryPending = false; this.changed(); };
    this.authority.offerHandoff = async () => { await work(); pending = undefined; this.metas.get(ME)!.meta.handoffRecoveryEd = new Uint8Array(32); await this.loadDevices(); };
    this.authority.discardUnpublishedCode = async () => { await work(); pending = undefined; };
    this.authority.resumeSecurityOperation = async () => { await work(); };
    this.handoffCandidates = async () => [{ id: ANDROID, recoveryEd: hash }];
    this.handoffAuthority = async (id) => { await work(); this.authorityDeviceId = id; this.removedDemoDevices.add(ME); await this.loadDevices(); };
    this.removeDevice = async id => { await work(); this.removedDemoDevices.add(id); await this.loadDevices(); };
    this.deleteMachineRecords = async id => { await work(); this.machines.delete(id); this.changed(); };
    Object.defineProperty(this, "startPairing", { configurable: true, value: async (_web: string, _replaces: string) => {
      await work(); let compared = false;
      return { code: "12345678", expires: BigInt(Date.now() + 5 * min), url: "https://app.example.com/#/pair?code=12345678",
        joined: async () => this.opts.get("pair") === "compare" ? { emoji: ["🌲", "🍋", "🚲", "🐳"], kind: DeviceKind.CLIENT } : undefined,
        approve: async (confirmed: boolean) => { if (!confirmed) throw new Error("请先确认图案"); await work(); compared = true; },
        finishPairingOperation: async () => { if (compared) await work(); }, cancel: async () => {},
      } as unknown as import("../authority-pairing").AuthorityPairing;
    } });
    if (this.opts.has("archive")) this.removedDemoDevices.add("mini");
  }

  private seed() {
    const empty = this.opts.get("empty");
    const machine = (
      id: string,
      hostname: string,
      online: boolean,
      meta: MessageInitShape<typeof MachineMetaSchema> = {},
    ) =>
      this.machines.set(id, {
        id,
        online,
        lastSeenMs: BigInt(online ? now : now - 2 * 60 * min),
        metaRev: 1n,
        meta: create(MachineMetaSchema, {
          hostname,
          home: "/Users/me",
          protocolVersion: PROTOCOL_VERSION,
          ...meta,
        }),
      });
    if (empty !== "none") {
      machine("mac", "MacBook", empty !== "offline", {
        codexVersion: "0.71.0",
        codexVerified: false,
        quotas: [
          {
            agent: Agent.CODEX,
            state: QuotaState.OK,
            plan: "Pro Lite",
            windows: [
              {
                name: "primary",
                usedPercent: 62,
                windowMinutes: 300n,
                resetsAtMs: BigInt(now + 3 * 60 * min),
              },
              {
                name: "secondary",
                usedPercent: 34,
                windowMinutes: 10080n,
                resetsAtMs: BigInt(now + 3 * 24 * 60 * min),
              },
            ],
          },
        ],
        agents: [
          {
            agent: Agent.CODEX,
            version: "0.71.0",
            verified: false,
            defaultMode: ApprovalMode.AUTO,
            defaultModeSource: DefaultModeSource.DAEMON_SETTINGS,
          },
          {
            agent: Agent.CLAUDE,
            version: "2.3.1",
            verified: true,
            defaultMode: ApprovalMode.ASK,
            defaultModeSource: DefaultModeSource.DAEMON_SETTINGS,
            defaultModeVaries: true,
          },
        ],
        capabilities: {
          imageUpload: true,
          maxImagesPerMessage: 6,
          maxUploadBytes: 20n * 1024n * 1024n,
          maxChunkBytes: 786432,
          approvalAlways: true,
        },
      });
      if (!empty) {
        machine("vps", "VPS-dev", true, {
          codexVersion: "0.70.2",
          codexVerified: true,
          capabilities: { approvalAlways: true },
        });
        machine("imac", "iMac", true, {
          codexVersion: "0.71.0",
          codexVerified: true,
          capabilities: {
            imageUpload: true,
            maxImagesPerMessage: 6,
            maxChunkBytes: 786432,
            approvalAlways: true,
          },
        });
        machine("mini", "Mac mini", false);
      }
    }
    if (empty) return;
    for (const [id, f] of Object.entries(fixtures())) {
      const info: Session = create(SessionSchema, {
        id,
        createdAt: timestampFromMs(f.updated - 5 * min),
        ...f.info,
      });
      if (this.opts.has("long")) {
        info.title =
          (info.title || "长会话标题") +
          "：检查输入区、项目列表和跨端状态同步是否正确处理长标题";
        if (info.cwd === "/Users/me/Code/foo")
          info.cwd =
            "/Users/me/Code/remote-agent-with-a-very-long-project-name";
      }
      this.sessions.set(id, {
        id,
        machineId: f.machine,
        key: {} as CryptoKey,
        info,
        metaRev: 1n,
        lastSeq: BigInt(f.events.length),
        updatedMs: BigInt(f.updated),
        endedMs: 0n,
      });
      this.evs.set(
        id,
        f.events.map((e, i) => {
          const [kind, origin] = Array.isArray(e) ? e : [e, undefined];
          return create(EventSchema, {
            seq: BigInt(i + 1),
            sessionId: id,
            time: timestampFromMs(f.updated - (f.events.length - i) * 20_000),
            kind,
            origin,
          });
        }),
      );
    }
    if (this.opts.has("longcmd"))
      for (const e of this.evs.get("s-appr") ?? [])
        if (e.kind.case === "approvalRequested" && e.kind.value.approvalId === "a1") {
          e.kind.value.command = longCommand;
          e.kind.value.description = e.kind.value.description.repeat(3);
        }
    if (this.opts.has("many"))
      for (let i = 0; i < 16; i++) {
        const id = `s-extra-${i}`;
        const info = create(SessionSchema, {
          title: `项目 ${i + 1} 的会话`,
          cwd: `/Users/me/Code/project-${i + 1}`,
          status: i < 7 ? SessionStatus.RUNNING : SessionStatus.IDLE,
          model: "gpt-6-astra",
          effort: "medium",
          createdAt: timestampFromMs(now - (i + 1) * min),
        });
        this.sessions.set(id, {
          id,
          machineId: "mac",
          key: {} as CryptoKey,
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(now - (i + 1) * min),
          endedMs: 0n,
        });
        this.evs.set(id, []);
      }
    if (this.opts.has("long"))
      for (const m of this.machines.values())
        if (m.meta) m.meta.hostname += "-with-a-very-long-machine-name";
    void this.drawPreviews();
    const out = (
      sid: string,
      sendId: string,
      text: string,
      status: "sending" | "sent" | "failed",
      error?: string,
      // Whether the computer may have taken it before the answer was lost.
      unknown?: boolean,
    ) => {
      this.outboxes.set(sid, [
        ...this.outbox(sid),
        {
          sendId,
          requestId: "r-" + sendId,
          deadline: now + 60_000,
          text,
          status,
          error,
          unknown,
        },
      ]);
    };
    out("s-run", "q-run", "顺便补一个测试", "sent");
    if (this.opts.has("draft"))
      this.drafts.set(
        "s-run",
        "请检查这段很长的路径 /Users/me/Code/foo/internal/generated/remoteagent/v1/client/session/messages/pending.ts，并补充测试",
      );
    if (this.opts.get("cancel") === "failed")
      this.cancels.set("q-multi", {
        state: "failed",
        error: "failed_precondition",
      });
    this.stored.prefs = applyOp(this.stored.prefs, {
      id: "seed-settings",
      kind: "settings",
      path: "/Users/me/Code/foo",
      at: now,
      settings: {
        agent: Agent.CODEX,
        machineId: "mac",
        sets: [
          {
            agent: Agent.CODEX,
            model: "gpt-6-astra",
            effort: "medium",
            mode: ApprovalMode.ASK,
            standardContext: false,
          },
        ],
      },
    });
    out("s-ask", "q-ask", "跑一下测试", "sent");
    out("s-popup", "q-popup", "看下这些脚本是做什么的", "sent");
    out("s-msg", "o-unconf", "把 lint 也跑一下", "sent");
    out("s-msg", "o-sent", "再看下 README 要不要改", "sent");
    // A send whose answer never came: it may have reached the computer.
    out("s-msg", "o-fail", "顺便补一个测试", "failed", "连接超时", true);
    out("s-msg", "o-sending", "最后提交", "sending");
    out("s-msg", "o-failed", "这两个页面底部的按钮都点不到", "sent");
    out("s-multi", "q-multi", "补充库存回滚测试。", "sent");
  }

  /** Fills in the demo pictures' previews, where there is a canvas. */
  private async drawPreviews() {
    for (const events of this.evs.values())
      for (const e of events) {
        const k = e.kind;
        const refs =
          k.case === "userMessage" ||
          k.case === "toolCall" ||
          k.case === "imageGenerated"
            ? k.value.images
            : [];
        for (const r of refs) {
          const l = looks.get(r.imageId);
          if (!l || r.sync === ImageSync.NAME_ONLY) continue;
          const bytes = await draw(l.look, r.previewWidth, r.previewHeight);
          if (bytes) r.preview = bytes;
        }
      }
    this.changed();
  }

  // Uploads take a moment per piece; a file named with "fail" fails.
  override async uploadImage(
    machineId: string,
    p: { bytes: Uint8Array; format: string },
    progress: (done: number, total: number) => void = () => undefined,
  ): Promise<{ imageId: string; expiresMs: number }> {
    const total = p.bytes.length;
    for (let done = 0; done < total; done += 786432) {
      progress(done, total);
      await this.pause(400);
    }
    progress(total, total);
    await this.pause(300);
    if (!this.machines.get(machineId)?.online)
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    if ((p as { name?: string }).name?.includes("fail"))
      throw new CommandError("unavailable", "连接超时");
    return {
      imageId: "up-" + Math.random().toString(16).slice(2, 10),
      expiresMs: Date.now() + 86400000,
    };
  }

  override async getImage(sid: string, imageId: string) {
    const cached = this.cachedImage(sid, imageId);
    if (cached) return cached;
    await this.pause(900);
    if (this.conn !== "online")
      throw new CommandError("unavailable", "和服务器断线了");
    if (imageId.startsWith("exp-")) return { kind: "expired" as const };
    if (imageId.startsWith("bad-")) return { kind: "bad" as const };
    const n = (this.attempts.get(imageId) ?? 0) + 1;
    this.attempts.set(imageId, n);
    if (imageId.startsWith("fail-") && n === 1)
      throw new CommandError("unavailable", "连接超时");
    const l = looks.get(imageId) ?? { look: "web" as Look, w: 1600, h: 1000 };
    const scale = Math.min(1, 2048 / Math.max(l.w, l.h));
    const bytes = await draw(
      l.look,
      Math.round(l.w * scale),
      Math.round(l.h * scale),
    );
    if (!bytes) return { kind: "missing" as const };
    const out = {
      kind: "ok" as const,
      blob: new Blob([bytes as Uint8Array<ArrayBuffer>], { type: "image/png" }),
      width: Math.round(l.w * scale),
      height: Math.round(l.h * scale),
      format: ImageFormat.PNG,
    };
    (this as unknown as { imageCache: Map<string, unknown> }).imageCache.set(
      sid + "/" + imageId,
      out,
    );
    return out;
  }

  // The link's state is the one asked for, whatever this browser's network.
  override setNetwork() {}

  override start() {
    const c = this.opts.get("conn");
    this.listed = this.opts.get("list") !== "fail";
    if (!this.listed) this.listError = "连接超时";
    if (c === "connecting") this.conn = "connecting";
    else if (c === "reconnecting")
      Object.assign(this, { conn: "offline", everOnline: true });
    else if (c === "removed")
      Object.assign(this, { conn: "offline", removed: true });
    else if (c === "nonetwork")
      Object.assign(this, {
        conn: "offline",
        everOnline: true,
        network: false,
      });
    else if (c === "noserver")
      Object.assign(this, {
        conn: "offline",
        everOnline: true,
        begun: true,
        waited: true,
        failures: 3,
        connError: "连接超时",
      });
    else if (c === "fail")
      Object.assign(this, {
        conn: "offline",
        begun: true,
        waited: true,
        failures: 3,
        connError: "证书无效",
      });
    else Object.assign(this, { conn: "online", everOnline: true });
    if (this.conn !== "online") this.lostAt = Date.now() - 70_000;
    this.changed();
    void this.loadDevices();
    void this.loadPrefs();
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
    return {
      loaded: true,
      error: sid === "s-hist" ? "连接超时" : "",
      broken: sid === "s-ended",
    };
  }

  override retryEvents() {}

  private push(sid: string, kind: EventKind) {
    const list = this.events(sid);
    list.push(
      create(EventSchema, {
        seq: BigInt(list.length + 1),
        sessionId: sid,
        time: timestampFromMs(Date.now()),
        kind,
      }),
    );
    if (kind?.case === "userMessage" && kind.value?.requestId)
      this.recorded(sid, [kind.value.requestId]);
    const s = this.sessions.get(sid);
    if (s) s.updatedMs = BigInt(Date.now());
    this.changed();
  }

  private patch(sid: string, p: Partial<Session>) {
    const s = this.sessions.get(sid);
    if (s?.info) s.info = { ...s.info, ...p };
    this.changed();
  }

  override async command(
    sid: string,
    kind: CommandKind,
    slot?: RequestSlot,
  ): Promise<CommandResult> {
    await this.pause(700);
    const cmd = create(CommandSchema, { kind }).kind;
    const ok = (
      kind: MessageInitShape<typeof CommandResultSchema>["kind"] = {
        case: undefined,
      },
    ) => create(CommandResultSchema, { kind });
    const s = this.sessions.get(sid)!;
    switch (cmd.case) {
      case "send": {
        const { text, requestId } = cmd.value;
        // Text with 失败 fails once; the retry with the same slot succeeds.
        const n = (this.attempts.get(requestId!) ?? 0) + 1;
        this.attempts.set(requestId!, n);
        if (text?.includes("失败") && n === 1)
          throw new CommandError("unknown", "连接超时");
        void slot;
        this.push(sid, {
          case: "inputState",
          value: {
            requestId,
            status: InputStatus.QUEUED,
            text,
            mode: cmd.value.mode,
          },
        });
        void (async () => {
          await this.pause(1500);
          if (text?.includes("终端"))
            return this.push(sid, {
              case: "inputState",
              value: { requestId, status: InputStatus.UNCONFIRMED },
            });
          this.push(sid, {
            case: "inputState",
            value: { requestId, status: InputStatus.SUBMITTED },
          });
          this.push(sid, { case: "userMessage", value: { text } });
          this.push(sid, { case: "turnStarted", value: {} });
          this.patch(sid, { status: SessionStatus.RUNNING });
          await this.pause(2000);
          this.push(sid, {
            case: "agentMessage",
            value: { text: "收到。这是演示数据里的回复。" },
          });
          this.push(sid, {
            case: "turnEnded",
            value: { outcome: TurnOutcome.COMPLETED, durationMs: 2000n },
          });
          this.patch(sid, { status: SessionStatus.IDLE });
        })();
        return ok();
      }
      case "resolveApproval": {
        const v = cmd.value;
        if (v.message?.includes("失败"))
          throw new CommandError("unknown", "连接超时");
        this.push(sid, {
          case: "approvalResolved",
          value: {
            approvalId: v.approvalId,
            decision: v.always
              ? ApprovalDecision.ALLOW_ALWAYS
              : v.allow
                ? ApprovalDecision.ALLOW
                : ApprovalDecision.DENY,
            message: v.always
              ? "Yes, and don't ask again for commands that start with `go test`"
              : v.message,
          },
        });
        this.patch(sid, {
          pendingApprovals: Math.max(0, (s.info?.pendingApprovals ?? 1) - 1),
        });
        return ok();
      }
      case "answerQuestion": {
        const q = this.events(sid).find(
          (e) =>
            e.kind.case === "questionAsked" &&
            e.kind.value.questionId === cmd.value.questionId,
        );
        const qa = q?.kind.case === "questionAsked" ? q.kind.value : undefined;
        if (qa && (cmd.value.questionIndex ?? 0) >= qa.questions.length - 1) {
          this.push(sid, {
            case: "questionAnswered",
            value: {
              questionId: cmd.value.questionId,
              answers: [{ answers: ["已选"] }],
            },
          });
          this.patch(sid, { pendingQuestions: 0 });
        }
        return ok();
      }
      case "answerPlan":
        this.patch(sid, { planOpen: false, collaborationMode: "default" });
        return ok();
      case "cancelInput":
        this.push(sid, {
          case: "inputState",
          value: {
            requestId: cmd.value.requestId,
            status: InputStatus.CANCELED,
          },
        });
        return ok();
      case "resumeInput":
        this.push(sid, {
          case: "inputState",
          value: {
            requestId: cmd.value.requestId,
            status: InputStatus.QUEUED,
            paused: false,
          },
        });
        return ok();
      case "interrupt":
        this.push(sid, {
          case: "turnEnded",
          value: { outcome: TurnOutcome.ABORTED },
        });
        this.patch(sid, { status: SessionStatus.IDLE });
        return ok();
      case "stopSession":
        this.push(sid, {
          case: "sessionState",
          value: { status: SessionStatus.EXITED, detail: "stopped" },
        });
        this.patch(sid, {
          status: SessionStatus.EXITED,
          statusDetail: "stopped",
        });
        return ok();
      case "resumeSession":
        await this.pause(1500);
        if (sid === "s-ended3")
          throw new CommandError(
            "failed_precondition",
            "目录 ~/Code/foo 不存在了",
          );
        this.patch(sid, { status: SessionStatus.STARTING });
        await this.pause(1500);
        this.patch(sid, { status: SessionStatus.IDLE });
        return ok();
      case "renameSession":
        this.patch(sid, { title: cmd.value.title });
        return ok();
      case "forkSession": {
        await this.pause(800);
        const src = this.sessions.get(sid)!;
        if (sid === "s-ended3")
          throw new CommandError(
            "failed_precondition",
            "目录 ~/Code/foo 不存在了",
          );
        const id = "s-fork-" + Math.random().toString(16).slice(2, 8);
        const history = this.events(sid).filter(
          (e) => e.kind.case !== "sessionState" && e.kind.case !== "inputState",
        );
        const info: Session = {
          ...(src.info ?? create(SessionSchema, {})),
          id,
          status: SessionStatus.STARTING,
          statusDetail: "",
          createdAt: timestampFromMs(Date.now()),
          forkedFrom: sid,
          forkedSeq: BigInt(history.length + 1),
        };
        const view: SessionView = {
          id,
          machineId: src.machineId,
          key: {} as CryptoKey,
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(Date.now()),
          endedMs: 0n,
        };
        setTimeout(() => {
          this.sessions.set(id, view);
          const list = this.events(id);
          for (const e of history)
            list.push(
              create(EventSchema, {
                ...e,
                seq: BigInt(list.length + 1),
                sessionId: id,
              }),
            );
          this.push(id, {
            case: "sessionForked",
            value: { sourceSessionId: sid, sourceTitle: src.info?.title ?? "" },
          });
        }, 300);
        setTimeout(() => this.patch(id, { status: SessionStatus.IDLE }), 2500);
        return create(CommandResultSchema, {
          kind: { case: "forkSession", value: { session: info } },
        });
      }
      case "setModel":
        if (cmd.value.model === "gpt-6-nova")
          throw new CommandError("unknown", "Codex 的菜单和预期不一样");
        this.push(sid, {
          case: "settingsChanged",
          value: { model: cmd.value.model, effort: cmd.value.effort },
        });
        this.patch(sid, { model: cmd.value.model, effort: cmd.value.effort });
        return ok();
      case "setApprovalMode": {
        const mode = cmd.value.mode;
        this.push(sid, { case: "settingsChanged", value: { mode } });
        const ctx =
          mode === ApprovalMode.AUTO
            ? ctxAuto
            : mode === ApprovalMode.PLAN
              ? ctxPlan
              : mode === ApprovalMode.FULL_ACCESS
                ? {
                    ...ctxAsk,
                    approvalPolicy: "never",
                    sandbox: "danger-full-access",
                  }
                : ctxAsk;
        this.patch(sid, ctx);
        return ok();
      }
      case "compact":
        await this.pause(1500);
        this.push(sid, { case: "settingsChanged", value: { compacted: true } });
        this.patch(sid, { contextTokens: 14n, contextWindow: 100n });
        return ok();
    }
    return ok();
  }

  // 连接检测: ?demo&ping=slow|fail
  override async pingServer(): Promise<number> {
    await this.pause(300);
    if (this.opts.get("ping") === "fail") throw new Error("没有回应");
    return this.opts.get("ping") === "slow"
      ? 840 + Math.round(Math.random() * 60)
      : 70 + Math.round(Math.random() * 30);
  }

  override async pingMachine(machineId: string): Promise<number> {
    await this.pause(machineId === "vps" ? 1800 : 600);
    if (machineId === "vps") return 2140;
    return 150 + Math.round(Math.random() * 60);
  }

  override async machineCommand(
    machineId: string,
    kind: CommandKind,
  ): Promise<CommandResult> {
    await this.pause(500);
    const cmd = create(CommandSchema, { kind }).kind;
    const m = this.machines.get(machineId);
    if (!m?.online)
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    if (this.opts.has("scheduled-final") && cmd.case === "schedule" && cmd.value.operation === "list")
      return create(CommandResultSchema, {kind:{case:"schedule",value:{resultJson:new TextEncoder().encode(JSON.stringify({tasks:scheduledFixture.tasks}))}}});
    switch (cmd.case) {
      case "listModels":
        if(this.opts.has("scheduled-final")) return create(CommandResultSchema,{kind:{case:"listModels",value:{defaultModel:"haiku",models:[{slug:"haiku",displayName:"haiku"}]}}});
        if (machineId === "vps") throw new CommandError("unknown", "连接超时");
        return create(CommandResultSchema, {
          kind: {
            case: "listModels",
            value: cmd.value.agent === Agent.CLAUDE ? claudeModels : models,
          },
        });
      case "getSettings":
        return create(CommandResultSchema, {
          kind: {
            case: "getSettings",
            value: {
              settings: this.settingsFor(),
              imageStorage: {
                uploadBytes: 200n << 20n,
                reservedBytes: 12n << 20n,
                protectedBytes: 80n << 20n,
                reclaimableBytes: 120n << 20n,
                cacheBytes: 32n << 20n,
                sessions: [],
              },
            },
          },
        });
      case "updateSettings":
        if (cmd.value.settings?.approvalTimeoutSeconds === 3000)
          throw new CommandError("unavailable", `${m.meta?.hostname} 不在线`);
        this.saved = create(DaemonSettingsSchema, cmd.value.settings);
        return create(CommandResultSchema, {
          kind: {
            case: "updateSettings",
            value: {
              settings: this.saved,
              imageStorage: {
                uploadBytes: cmd.value.cleanImages ? 80n << 20n : 200n << 20n,
                reservedBytes: 12n << 20n,
                protectedBytes: 80n << 20n,
                reclaimableBytes: cmd.value.cleanImages ? 0n : 120n << 20n,
                cacheBytes: cmd.value.cleanImages ? 0n : 32n << 20n,
                sessions: [],
              },
            },
          },
        });
      case "listDirectories": {
        const path = cmd.value.path || "/Users/me";
        if (path.endsWith("Downloads"))
          throw new CommandError("permission_denied", "没有读取权限");
        const parent = path === "/" ? "" : path.replace(/\/[^/]+$/, "") || "/";
        return create(CommandResultSchema, {
          kind: {
            case: "listDirectories",
            value: { path, parent, home: "/Users/me", dirs: tree[path] ?? [] },
          },
        });
      }
      case "createSession": {
        const v = cmd.value;
        if (v.prompt?.includes("失败"))
          throw new CommandError("failed_precondition", "tmux 没有安装");
        const id = "s-new-" + Math.random().toString(16).slice(2, 8);
        const info = create(SessionSchema, {
          id,
          cwd: v.cwd,
          status: SessionStatus.STARTING,
          title: v.prompt?.split("\n")[0] ?? "",
          agent: v.agent,
          model: v.model,
          effort: v.effort,
          createdAt: timestampFromMs(Date.now()),
        });
        const view: SessionView = {
          id,
          machineId,
          key: {} as CryptoKey,
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(Date.now()),
          endedMs: 0n,
        };
        setTimeout(() => {
          this.sessions.set(id, view);
          if (v.prompt)
            this.push(id, {
              case: "inputState",
              value: { requestId: "p-" + id, status: InputStatus.QUEUED },
            });
          this.changed();
        }, 300);
        setTimeout(() => {
          this.patch(id, {
            status: SessionStatus.IDLE,
            model: "gpt-6-astra",
            effort: "medium",
            ...ctxAsk,
          });
          if (v.prompt) {
            this.push(id, {
              case: "inputState",
              value: { requestId: "p-" + id, status: InputStatus.SUBMITTED },
            });
            this.push(id, { case: "userMessage", value: { text: v.prompt } });
          }
        }, 4000);
        return create(CommandResultSchema, {
          kind: { case: "createSession", value: { session: info } },
        });
      }
    }
    return create(CommandResultSchema, {});
  }

  private saved?: ReturnType<typeof create<typeof DaemonSettingsSchema>>;

  private settingsFor() {
    return (this.saved ??= create(DaemonSettingsSchema, {
      imageUploadQuotaBytes: 1n << 30n,
      approvalTimeoutSeconds: 600,
      defaultModel: "gpt-6-astra",
      defaultEffort: "medium",
      defaultMode: ApprovalMode.AUTO,
      claude: {
        model: "claude-sonnet-5-5",
        effort: "high",
        mode: ApprovalMode.ASK,
      },
    }));
  }

  override async openTerminal(
    sid: string,
    onFrame: (f: TerminalFrame) => void,
    onClose: (reason: string) => void,
  ): Promise<TermConn> {
    await this.pause(400);
    let open = true;
    const popup =
      this.sessions.get(sid)?.info?.status === SessionStatus.BLOCKED;
    const own = { cols: popup ? 42 : 120, rows: popup ? 30 : 40 };
    const snapshot = (size: { cols: number; rows: number }) => {
      if (!open) return;
      const data = new TextEncoder().encode(
        popup ? trust : screen(this.sessions.get(sid)?.info?.title ?? ""),
      );
      onFrame({
        $typeName: "remoteagent.v1.TerminalFrame",
        kind: {
          case: "snapshot",
          value: {
            $typeName: "remoteagent.v1.TerminalSnapshot",
            data,
            ...size,
          },
        },
      });
    };
    setTimeout(() => {
      snapshot(own);
      if (this.opts.get("term") === "closed")
        setTimeout(() => open && onClose("stream closed: output backlog"), 800);
      // New live output while reading, so the "有新输出" pill shows.
      if (this.opts.get("term") === "history")
        for (const at of [3000, 6000])
          setTimeout(() => {
            if (open)
              onFrame({
                $typeName: "remoteagent.v1.TerminalFrame",
                kind: {
                  case: "output",
                  value: new TextEncoder().encode(
                    `\r\n\x1b[32mnew output after ${at / 1000}s\x1b[0m\r\n`,
                  ),
                },
              });
          }, at);
    }, 200);
    return {
      // Like the daemon: a resize is answered with a snapshot at the new size.
      send: (f) => {
        if (f.kind?.case === "historyRequest") {
          const req = f.kind.value;
          const end = req.cursor ? Number(req.cursor) : 1200;
          const start = Math.max(0, end - 200);
          const lines = Array.from({ length: end - start }, (_, i) =>
            historyLine(start + i + 1),
          );
          setTimeout(
            () =>
              open &&
              onFrame({
                $typeName: "remoteagent.v1.TerminalFrame",
                kind: {
                  case: "historyPage",
                  value: {
                    $typeName: "remoteagent.v1.TerminalHistoryPage",
                    requestId: req.requestId ?? "",
                    lines,
                    nextCursor: start ? String(start) : "",
                    error: "",
                  },
                },
              }),
            200,
          );
          return;
        }
        if (f.kind?.case !== "resize") return;
        const r = f.kind.value;
        setTimeout(
          () =>
            snapshot(r.fit ? { cols: r.cols ?? 0, rows: r.rows ?? 0 } : own),
          150,
        );
      },
      close: () => {
        open = false;
      },
    };
  }

  // --- devices and project preferences, in memory ---

  private metas = new Map<
    string,
    { rev: bigint; meta: ReturnType<typeof create<typeof DeviceMetaSchema>> }
  >([
    [
      ME,
      {
        rev: 1n,
        meta: create(DeviceMetaSchema, {
          platform: "web",
          description: "Chrome on macOS",
        }),
      },
    ],
    [
      ANDROID,
      {
        rev: 1n,
        meta: create(DeviceMetaSchema, {
          platform: "android",
          description: "Android 16 · Pixel 9",
        }),
      },
    ],
  ]);

  override async loadDevices() {
    this.devices.clear();
    for (const m of this.machines.values())
      this.devices.set(m.id, {
        id: m.id,
        kind: DeviceKind.MACHINE,
        createdMs: 0n,
        metaRev: 0n,
        lastSeenMs: 0n,
      });
    for (const id of [ME, ANDROID, BROWSER]) {
      const m = this.metas.get(id);
      this.devices.set(id, {
        id,
        kind: DeviceKind.CLIENT,
        createdMs: BigInt(
          Date.now() - (id === ME ? 6 : id === ANDROID ? 21 : 300) * 86_400_000,
        ),
        metaRev: m?.rev ?? 0n,
        meta: m?.meta,
        lastSeenMs:
          id === BROWSER
            ? 0n
            : BigInt(Date.now() - (id === ME ? 0 : 86_400_000)),
      });
    }
    for (const id of this.removedDemoDevices) this.devices.delete(id);
    if (this.opts.get("role") === "machine" && this.devices.get(ME)) this.devices.get(ME)!.kind = DeviceKind.MACHINE;
    this.devicesStatus = "ready";
    this.changed();
  }

  override async renameDevice(id: string, name: string) {
    await this.pause(500);
    const cur = this.metas.get(id);
    const meta = create(DeviceMetaSchema, {
      ...(cur?.meta ?? { platform: "web" }),
      name: name.trim(),
    });
    this.metas.set(id, { rev: (cur?.rev ?? 0n) + 1n, meta });
    await this.loadDevices();
  }

  private stored: PrefsSnapshot = {
    revision: 1n,
    prefs: [
      ["pin", "/Users/me/Code/foo"],
      ["hide", "/Users/me/Code/demo"],
      ["hide", "/Users/me/work/experiments"],
      ["hide", "/Users/me/Code/old-site"],
    ].reduce(
      (p, [kind, path], i) =>
        applyOp(p, {
          id: "seed" + i,
          kind: kind as "pin",
          path,
          at: now - 86_400_000,
        }),
      emptyPrefs(),
    ),
  };

  protected override prefsStore = {
    get: async () => {
      await this.pause(300);
      return this.stored;
    },
    put: async (expected: bigint, next: ProjectPrefs) => {
      await this.pause(700);
      if (this.opts.get("prefs") === "fail")
        throw new CommandError("unavailable", "服务器没有响应");
      if (expected !== this.stored.revision)
        return { applied: false, current: this.stored };
      this.stored = { revision: expected + 1n, prefs: next };
      return { applied: true, current: this.stored };
    },
  };

  override async loadPrefs() {
    this.prefs = { ...this.prefs, status: "ready", snap: this.stored };
    this.changed();
  }

  override async pushKey(): Promise<string> {
    throw new CommandError("unavailable", "服务器没有响应");
  }
}
