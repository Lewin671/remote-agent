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
//   ?demo&one-machine=1                     an account with one computer only
//   ?demo&long=1                            long project, machine and session names
//   ?demo&many=1&pick=1                     many sessions/projects, picker search
//   ?demo&project-settings=1#/p/<encoded path>   project settings sheet
//   &history-prefix                        restore an incomplete cached prefix before the complete history
//   ?demo&backfill=800&history-delay=500#/s/s-run  cold long history: empty, latest page, then full history
//   ?demo&backfill=60#/s/s-run              60 turns of tools and replies, of which the last 10 show first and the rest load above 1.5 s later
//   ?demo&files=1#/s/s-run             attachment menu on a file-capable machine
//   ?demo&draft=1&sendmenu=1#/s/s-run       running draft and long-press menu
//   ?demo&queue-all=1#/s/s-multi           queue, held, paused, other device
//   ?demo&cancel=failed&queue-all=1#/s/s-multi  cancel retry
//   ?demo&quota=fresh|old|unsupported|failed|warning#/q/mac  quota states
//   ?demo&deny=1                          inline optional denial reason
//   ?demo&longcmd=1#/s/s-appr               an approval of a 40-line command with a long description
//   ?demo&direct=direct|file|read|measuring|relay|relay-faster|trying|none|peer_unsupported|no_response|no_traversal|failed|off|unsupported|mix&speeds=both|direct|relay|none#/m/mac
//                                         the direct path of the computers: 「连接」 on the computer's page, the connection check's lines (direct-demo.ts)
//   ?demo&diag=ipv6|all|nolocal|nostun|noremote|dtls|channel|refused|noreply|limited|budget|connected|trying#/m/mac
//                                         the 「诊断信息」 row of the 「连接」 group (open it to see the sheet): one failure class each (direct-demo.ts);
//                                         add &diagsheet=1 to open the sheet at once
//   ?demo&conncheck=1#/settings            opens the connection check sheet at once
//   ?demo&notify=1#/s/s-notify              scheduled pushes' receipts: the computer's copy, the agent's own, a stopped run's

import { localeStore, t, type Locale } from "../i18n";
import { MemoryAttachmentStore, type AttachmentStore } from "../attachment-store";
import { bytesToHex, MemorySharedFileStore, SHARED_FILE_META_VERSION } from "../shared-file-store";
import { seedDirect } from "./direct-demo";
import { buildDemoUsageResponse } from "./usage-demo";
import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
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
  SharedFileSchema,
  ToolKind,
  TurnOutcome,
  type Event,
} from "../gen/remoteagent/v1/event_pb";
import {
  DaemonSettingsSchema,
  ImageUploadState,
  ImageUploadStatusSchema,
  ListModelsResponseSchema,
  SessionSchema,
  UsageRangePreset,
  UsageStatsResponseSchema,
  type Session,
} from "../gen/remoteagent/v1/local_pb";
import {
  CommandResultSchema,
  CommandSchema,
  AgentQuotaSchema,
  MachineMetaSchema,
  ProjectSummarySchema,
  type CommandResult,
  type TerminalFrame,
} from "../gen/remoteagent/v1/remote_pb";
import {
  CommandError,
  Remote,
  RequestSlot,
  type CommandKind,
  type MachineView,
  type SessionView,
  type StreamLegCallbacks,
  type TermConn,
} from "../remote";
import { getCoreHost, sessionDraftScope } from "../core/host";
import {
  AccountPrefsFetchedEventSchema,
  AccountPrefsPutResultEventSchema,
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  DevicesFetchedEventSchema,
  EventSchema as CoreEventSchema,
  EventsAppendedFrameSchema,
  EventsPageFetchedEventSchema,
  ListsFetchedEventSchema,
  OutcomeSource,
  PlainAccountPrefsSchema,
  PlainDeviceSchema,
  PlainEventSchema,
  PlainMachineSchema,
  PlainSessionSchema,
  SessionByIdFetchedEventSchema,
  TimerKind,
  TokenResultEventSchema,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Event as CoreEvent,
  type RpcEffect,
  type TokenResultEvent,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  DemoDeviceSchema,
  DemoMachineSchema,
  DemoSecuritySchema,
  DemoSessionSchema,
  SeedDemoWorldEventSchema,
  SecurityBootstrapKind,
  SecurityHandoffCandidateSchema,
  SecurityPairingInfoSchema,
  SecurityPendingOpSummarySchema,
  SecurityWorkflowStatusSchema,
  type SecurityHandoffCandidate,
  type SecurityPairingInfo,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
import { DeviceKind, SessionCursorSchema } from "../gen/remoteagent/v1/relay_pb";
import {
  AgentSettingsSchema,
  DefaultModeSource,
  DeviceMetaSchema,
  LiveSnapshotSchema,
  ProjectPrefSchema,
  ProjectSettingsSchema,
  QuotaState,
  type ProjectPrefs,
  ProjectPrefsSchema,
} from "../gen/remoteagent/v1/remote_pb";
import { PEER_PROTOCOL_VERSION } from "../protocol";
import { DemoStatus, demoDisk, scenarioOf } from "./machine-status-demo";
import scheduledFixture from "../../../testdata/schedules/clients.json";
import { ScheduleCacheLoadedEventSchema } from "../gen/remoteagent/core/v1/schedules_view_pb";

interface DemoScheduleTask {
  id: string;
  revision: number;
  status: string;
  reason?: string;
  source?: string;
  created_at: string;
  updated_at?: string;
  dispatches?: number;
  next_run_at?: string;
  runs?: Array<Record<string, any>>;
  spec: {
    title: string;
    message: string;
    notify?: string;
    target: Record<string, any>;
    trigger: Record<string, any>;
  };
  [key: string]: any;
}

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
    "s-agy": {
      machine: "mac",
      updated: now - 18 * min,
      info: {
        title: "梳理状态栏组件",
        cwd: "/Users/me/Code/web",
        status: SessionStatus.IDLE,
        agent: Agent.ANTIGRAVITY,
        model: "gemini-3.1-pro-high",
        effort: "high",
        mode: ApprovalMode.ASK,
        fullAccessUnavailable: true,
        contextTokens: 294_000n,
        contextWindow: 1_048_000n,
        preview: "状态栏和抽屉入口已合并。",
      },
      events: [
        { case: "userMessage", value: { text: "梳理状态栏组件" } },
        { case: "agentMessage", value: { text: "状态栏和抽屉入口已合并。" } },
      ],
    },
    "s-agy-full": {
      machine: "mac",
      updated: now - 19 * min,
      info: {
        title: "批量格式化配置",
        cwd: "/Users/me/Code/web",
        status: SessionStatus.IDLE,
        agent: Agent.ANTIGRAVITY,
        model: "gemini-3-flash",
        effort: "medium",
        mode: ApprovalMode.FULL_ACCESS,
        modeLocked: true,
        contextTokens: 120_000n,
        contextWindow: 1_048_000n,
        preview: "已完成所有配置文件的格式化。",
      },
      events: [
        { case: "userMessage", value: { text: "批量格式化配置文件" } },
        { case: "agentMessage", value: { text: "已完成所有配置文件的格式化。" } },
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
const agyModels = create(ListModelsResponseSchema, {
  defaultModel: "gemini-3.1-pro-high",
  models: [
    {
      slug: "gemini-3.1-pro-high",
      displayName: "Gemini 3.1 Pro (High)",
      efforts: ["low", "medium", "high"],
      defaultEffort: "high",
    },
    {
      slug: "gemini-3.1-pro-low",
      displayName: "Gemini 3.1 Pro (Low)",
      efforts: ["low", "medium", "high"],
      defaultEffort: "low",
    },
    {
      slug: "gemini-3-flash",
      displayName: "Gemini 3 Flash",
      efforts: ["low", "medium", "high"],
      defaultEffort: "high",
    },
    {
      slug: "claude-sonnet-4-6",
      displayName: "Claude Sonnet 4.6",
      efforts: ["low", "medium", "high"],
      defaultEffort: "high",
    },
    {
      slug: "claude-opus-4-6-thinking",
      displayName: "Claude Opus 4.6 (Thinking)",
      efforts: ["low", "medium", "high"],
      defaultEffort: "high",
    },
    {
      slug: "gpt-oss-120b-medium",
      displayName: "GPT-OSS 120B (Medium)",
      efforts: ["medium"],
      defaultEffort: "medium",
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
  // Locally authored samples, with no relay or account data.
  override readonly sharedFileStore = new MemorySharedFileStore();
  override readonly attachmentStore = new MemoryAttachmentStore();
  override demoFlag(name: string): boolean { return this.opts.has(name); }
  private demoUploads = new Map<string, { fileName: string; totalBytes: bigint; persistedBytes: bigint; imageId: string; fileId: string; expiresMs: bigint; completed: boolean }>();
  private demoUploadSeq = 0;
  private demoMachines = new Map<string, MachineView>();
  private demoSessions = new Map<string, SessionView>();
  private demoDevices = new Map<string, import("../devices").DeviceInfo>();
  private demoStream?: { legId: bigint; callbacks: StreamLegCallbacks; ready: boolean };
  private demoSeedSynced = false;
  private demoSyncing = false;
  private demoHasMoreSessions = false;
  private demoBootConsumed = false;
  private demoListRetried = false;
  private syncBootOpId = 0n;
  private syncTokenOpId = 0n;
  private syncRpcQueue: RpcEffect[] = [];
  private evs = new Map<string, Event[]>();
  private attempts = new Map<string, number>();
  private pause = sleep;

  private plainMachine = (m: MachineView) =>
    create(PlainMachineSchema, { machineId: m.id, online: m.online, lastSeenMs: m.lastSeenMs, meta: m.meta, metaRev: m.metaRev });
  private demoCheckpoint(s: SessionView) {
    if (s.checkpoint) return s.checkpoint;
    const evs = this.evs.get(s.id);
    if (!evs?.length) return undefined;
    const resolved = new Set<string>();
    const answered = new Set<string>();
    let planEv: Event | undefined;
    for (const e of evs) {
      if (e.kind.case === "approvalResolved") resolved.add(e.kind.value.approvalId);
      else if (e.kind.case === "questionAnswered") answered.add(e.kind.value.questionId);
      else if (e.kind.case === "planProposed") planEv = e;
      else if (e.kind.case === "planAnswered") planEv = undefined;
    }
    const actions = evs.filter(
      (e) =>
        (e.kind.case === "approvalRequested" && !resolved.has(e.kind.value.approvalId)) ||
        (e.kind.case === "questionAsked" && !e.kind.value.async && !answered.has(e.kind.value.questionId)),
    );
    const planText = planEv?.kind.case === "planProposed" ? planEv.kind.value.text : "";
    if (!actions.length && !s.info?.planOpen) return undefined;
    return create(LiveSnapshotSchema, {
      session: s.info,
      running: s.info?.status === SessionStatus.RUNNING || s.info?.status === SessionStatus.STARTING,
      actions,
      planEventSeq: planEv?.seq ?? 0n,
      planScreen: s.info?.planOpen ? planText : "",
      planScreenHash: s.info?.planOpen ? new Uint8Array(32).fill(1) : new Uint8Array(0),
    });
  }
  private plainSession = (s: SessionView) => {
    const checkpoint = this.demoCheckpoint(s);
    return create(PlainSessionSchema, { sessionId: s.id, machineId: s.machineId, lastSeq: s.lastSeq, updatedMs: s.updatedMs, listedEndedMs: s.endedMs, meta: s.info, metaRev: s.metaRev, checkpoint, checkpointInvalid: !checkpoint });
  };
  private plainDevice = (d: import("../devices").DeviceInfo) =>
    create(PlainDeviceSchema, { deviceId: d.id, kind: d.kind, createdMs: d.createdMs, lastSeenMs: d.lastSeenMs, meta: d.meta, metaRev: d.metaRev });
  private demoPrefs = () => create(PlainAccountPrefsSchema, { revision: this.stored.revision, prefs: this.stored.prefs });
  private demoScope = () => { try { return localStorage.getItem("ra-scope") || "all"; } catch { return "all"; } };
  private demoToken(opId: bigint) {
    const conn = this.opts.get("conn");
    const removed = conn === "removed";
    return create(TokenResultEventSchema, { opId, token: conn === "fail" || removed ? "" : "demo-token", expiresMs: BigInt(Date.now() + 30 * 86_400_000), removed, errorCode: conn === "fail" ? "invalid_argument" : "", error: conn === "fail" ? "证书无效" : "" });
  }
  private demoSecurity() {
    const removed = this.opts.get("conn") === "removed";
    return create(DemoSecuritySchema, {
      isMember: !removed,
      removed,
      selfDeviceId: ME,
      authorityDeviceId: this.authorityDeviceId || ANDROID,
      trustedMachineIds: [...this.demoMachines.keys()],
      trustedClientIds: [ME, ANDROID, BROWSER].filter((id) => !this.removedDemoDevices.has(id)),
      archiveMachineIds: [...this.demoMachines.keys()],
      headEpoch: 1n,
      leaseEndMs: this.opts.get("cliswitches") === "lease" ? BigInt(Date.now() - 60_000) : 0n,
      recoveryPending: this.demoRecoveryPending,
      recoveryReadyMs: this.demoRecoveryReadyMs,
    });
  }
  private demoBoot(opId: bigint) {
    return create(CacheBootstrapLoadedEventSchema, { opId, homeScopeToken: this.demoScope(), scopeHmacKey: new Uint8Array(32) });
  }
  private demoWorld(includeWorld: boolean) {
    if (!includeWorld) {
      return create(SeedDemoWorldEventSchema, { security: this.demoSecurity() });
    }
    return create(SeedDemoWorldEventSchema, {
      machines: [...this.demoMachines.values()].map((m) =>
        create(DemoMachineSchema, { machineId: m.id, online: m.online, lastSeenMs: m.lastSeenMs, meta: m.meta }),
      ),
      sessions: [...this.demoSessions.values()].map((s) =>
        create(DemoSessionSchema, {
          sessionId: s.id,
          machineId: s.machineId,
          lastSeq: s.lastSeq,
          updatedMs: s.updatedMs,
          listedEndedMs: s.endedMs,
          meta: s.info,
          checkpoint: this.demoCheckpoint(s),
        }),
      ),
      devices: [...this.demoDevices.values()].map((d) =>
        create(DemoDeviceSchema, { deviceId: d.id, kind: d.kind, createdMs: d.createdMs, lastSeenMs: d.lastSeenMs, meta: d.meta }),
      ),
      prefs: this.stored.prefs,
      security: this.demoSecurity(),
    });
  }
  private pushFrame(legId: bigint, frame: NonNullable<import("../gen/remoteagent/core/v1/app_pb").StreamFrameEvent["frame"]>) {
    getCoreHost(this).dispatch(create(CoreEventSchema, { event: { case: "streamFrame", value: { legId, frame } } }));
  }

  /** Syncs the current demo fixtures into Core so Home/Project/NewSession/SyncStateView update. */
  syncDemoToCore(forceRefresh = false) {
    if (!globalThis.__remoteAgentCore) return;
    const host = getCoreHost(this);
    const conn = this.opts.get("conn");
    const removed = conn === "removed";
    this.network = conn !== "nonetwork";
    const drainRpc = () => { while (this.syncRpcQueue.length > 0) host.dispatch(this.resolveSyncRpc(this.syncRpcQueue.shift()!)); };
    if (!this.demoSeedSynced) {
      this.demoSeedSynced = true;
      this.syncBootOpId = this.syncTokenOpId = 0n;
      this.syncRpcQueue = [];
      this.demoSyncing = true;
      try {
        host.start();
        this.demoBootConsumed = true;
        host.dispatch(create(CoreEventSchema, { event: { case: "cacheBootstrapLoaded", value: this.demoBoot(this.syncBootOpId) } }));
        host.dispatch(create(CoreEventSchema, { event: { case: "seedDemoWorld", value: this.demoWorld(this.opts.get("list") !== "fail") } }));
        host.dispatch(create(CoreEventSchema, { event: { case: "tokenResult", value: this.demoToken(this.syncTokenOpId) } }));
        drainRpc();
        if (conn === "noserver" && this.demoStream) {
          this.demoStream.callbacks.onClosed(this.demoStream.legId, { policyViolation: false, errorCode: "unavailable", error: "连接超时", unresolvedHost: false });
          host.fireActiveTimer(TimerKind.LINK_GRACE, "display");
          host.fireActiveTimer(TimerKind.LINK_GRACE);
        }
      } finally { this.demoSyncing = false; this.syncRpcQueue = []; }
      this.changed();
      return;
    }
    if (this.demoStream?.ready && !removed && conn !== "fail") {
      const legId = this.demoStream.legId;
      const curView = host.viewSyncState();
      const wantedIds = new Set(this.demoMachines.keys());
      for (const prev of curView.machines) if (!wantedIds.has(prev.machineId)) this.pushFrame(legId, { case: "machineRemovedId", value: prev.machineId });
      for (const m of this.demoMachines.values()) this.pushFrame(legId, { case: "machineUpdated", value: this.plainMachine(m) });
      const wantedSessions = [...this.demoSessions.values()];
      const wantedSids = new Set(wantedSessions.map((s) => s.id));
      if (forceRefresh || curView.sessions.some((prev) => !wantedSids.has(prev.sessionId))) {
        this.syncRpcQueue = [];
        this.demoSyncing = true;
        try {
          host.dispatch(create(CoreEventSchema, { event: { case: "streamReady", value: { legId, serverTimeMs: BigInt(Date.now()) } } }));
          drainRpc();
        } finally { this.demoSyncing = false; this.syncRpcQueue = []; }
      } else {
        for (const s of wantedSessions) this.pushFrame(legId, { case: "sessionUpdated", value: this.plainSession(s) });
      }
      for (const d of this.demoDevices.values()) this.pushFrame(legId, { case: "deviceUpdated", value: this.plainDevice(d) });
      this.pushFrame(legId, { case: "accountPrefsUpdated", value: this.demoPrefs() });
    }
    this.changed();
  }

  override ensureToken(opId: bigint): Promise<TokenResultEvent> {
    if (this.demoSyncing) { this.syncTokenOpId = opId; return new Promise(() => {}); }
    return Promise.resolve(this.demoToken(opId));
  }
  override securityConfig() { return undefined; }
  override openStreamLeg(legId: bigint, _token: string, callbacks: StreamLegCallbacks) {
    this.demoStream = { legId, callbacks, ready: false };
    const conn = this.opts.get("conn");
    if (conn === "connecting") return;
    if ((conn === "noserver" && !this.demoSyncing) || ["reconnecting", "nonetwork", "fail", "removed"].includes(conn ?? "")) {
      callbacks.onClosed(legId, { policyViolation: false, errorCode: "unavailable", error: "连接超时", unresolvedHost: false });
      return;
    }
    this.demoStream.ready = true;
    callbacks.onReady(legId, BigInt(Date.now()));
  }
  override closeStreamLeg(legId: bigint) { if (this.demoStream?.legId === legId) this.demoStream = undefined; }

  setDemoMainOnline(online: boolean) {
    const stream = this.demoStream;
    const host = getCoreHost(this);
    if (online) {
      this.opts.delete("conn");
      this.syncTokenOpId = 0n;
      this.syncRpcQueue = [];
      this.demoSyncing = true;
      try {
        if (stream && !stream.ready) {
          stream.ready = true;
          stream.callbacks.onReady(stream.legId, BigInt(Date.now()));
        } else {
          host.reconnectNow();
          if (this.syncTokenOpId > 0n) {
            host.dispatch(create(CoreEventSchema, { event: { case: "tokenResult", value: this.demoToken(this.syncTokenOpId) } }));
          }
        }
        while (this.syncRpcQueue.length > 0) {
          host.dispatch(this.resolveSyncRpc(this.syncRpcQueue.shift()!));
        }
      } finally {
        this.demoSyncing = false;
        this.syncRpcQueue = [];
      }
      return;
    }
    this.opts.set("conn", "connecting");
    if (stream) {
      this.demoStream = undefined;
      stream.ready = false;
      stream.callbacks.onClosed(stream.legId, { policyViolation: false, errorCode: "unavailable", error: "", unresolvedHost: false });
    }
  }
  override readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent> {
    if (this.demoSyncing || this.demoBootConsumed) { this.syncBootOpId = opId; this.demoBootConsumed = false; return new Promise(() => {}); }
    return Promise.resolve(this.demoBoot(opId));
  }
  override readEventsCache(opId: bigint, sessionId: string): Promise<CacheEventsLoadedEvent> {
    return Promise.resolve(create(CacheEventsLoadedEventSchema, { opId, sessionId }));
  }
  override async writeCache(_eff: CacheWriteEffect) {}
  override async deleteCache(_eff: CacheDeleteEffect) {}
  override readScheduleCache(opId: bigint, machineId: string) {
    return Promise.resolve(create(ScheduleCacheLoadedEventSchema, { opId, machineId }));
  }
  override async deleteScheduleCache(_machineId: string) {}
  private readonly demoSchedules = new Map<string, DemoScheduleTask[]>();

  private resolveSyncRpc(eff: RpcEffect): CoreEvent {
    const opId = eff.opId;
    if (eff.kind.case === "fetchLists") {
      const fail = this.opts.get("list") === "fail" && !this.demoListRetried;
      return create(CoreEventSchema, {
        event: {
          case: "listsFetched",
          value: create(ListsFetchedEventSchema, fail ? { opId, error: "连接超时" } : {
            opId,
            machines: [...this.demoMachines.values()].map(this.plainMachine),
            sessions: [...this.demoSessions.values()].map(this.plainSession),
            devices: [...this.demoDevices.values()].map(this.plainDevice),
            next: this.demoHasMoreSessions ? create(SessionCursorSchema, { endedMs: 1n, sessionId: "z" }) : undefined,
          }),
        },
      });
    }
    if (eff.kind.case === "getAccountPrefs") {
      return create(CoreEventSchema, { event: { case: "accountPrefsFetched", value: create(AccountPrefsFetchedEventSchema, { opId, prefs: this.demoPrefs() }) } });
    }
    return create(CoreEventSchema, {});
  }

  override async executeRpc(eff: RpcEffect): Promise<CoreEvent> {
    if (this.demoSyncing) { this.syncRpcQueue.push(eff); return new Promise(() => {}); }
    const opId = eff.opId;
    switch (eff.kind.case) {
      case "fetchLists":
      case "getAccountPrefs":
        return this.resolveSyncRpc(eff);
      case "fetchSessionById": {
        const s = this.demoSessions.get(eff.kind.value.sessionId);
        return create(CoreEventSchema, { event: { case: "sessionByIdFetched", value: create(SessionByIdFetchedEventSchema, { opId, session: s ? this.plainSession(s) : undefined, error: "" }) } });
      }
      case "getEvents": {
        const { sessionId, afterSeq, limit } = eff.kind.value;
        if (sessionId === "s-hist") return create(CoreEventSchema, { event: { case: "eventsPageFetched", value: create(EventsPageFetchedEventSchema, { opId, sessionId, error: "连接超时" }) } });
        const all = this.evs.get(sessionId) ?? [];
        const slice = all.filter((e) => e.seq > afterSeq).slice(0, limit || 500);
        const more = slice.length > 0 && slice.at(-1)!.seq < (all.at(-1)?.seq ?? 0n);
        return create(CoreEventSchema, {
          event: {
            case: "eventsPageFetched",
            value: create(EventsPageFetchedEventSchema, {
              opId, sessionId, more,
              events: slice.map((e) => create(PlainEventSchema, { seq: e.seq, event: e, verified: true, retired: sessionId === "s-ended", broken: sessionId === "s-ended" })),
            }),
          },
        });
      }
      case "putAccountPrefs": {
        const { expectedRevision, nextPrefs } = eff.kind.value;
        await this.pause(200);
        if (this.opts.get("prefs") === "fail") return create(CoreEventSchema, { event: { case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, error: "服务器没有响应" }) } });
        if (expectedRevision !== this.stored.revision) return create(CoreEventSchema, { event: { case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, applied: false, current: this.demoPrefs() }) } });
        this.stored = { revision: expectedRevision + 1n, prefs: nextPrefs ?? this.stored.prefs };
        return create(CoreEventSchema, { event: { case: "accountPrefsPutResult", value: create(AccountPrefsPutResultEventSchema, { opId, applied: true, current: this.demoPrefs() }) } });
      }
      case "fetchDevices":
        return create(CoreEventSchema, { event: { case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId, devices: [...this.demoDevices.values()].map(this.plainDevice) }) } });
      default:
        return create(CoreEventSchema, { event: { case: "devicesFetched", value: create(DevicesFetchedEventSchema, { opId }) } });
    }
  }

  override async executeCommandAttempt(eff: DispatchCommandEffect): Promise<CommandOutcomeEvent> {
    try {
      const cmd = fromBinary(CommandSchema, eff.commandBytes).kind;
      const slot = eff.requestId ? new RequestSlot(eff.requestId, Date.now() + 30_000, undefined, false) : undefined;
      const isMachineUpload =
        cmd.case === "beginImageUpload" ||
        cmd.case === "putImageChunk" ||
        cmd.case === "getImageUploadStatus" ||
        cmd.case === "completeImageUpload" ||
        cmd.case === "abortImageUpload";
      const result = !eff.sessionId && cmd.case === "listModels" && this.listModels !== Remote.prototype.listModels
        ? create(CommandResultSchema, { kind: { case: "listModels", value: await this.listModels(eff.machineId, cmd.value.agent, true) } })
        : !eff.sessionId && cmd.case === "getSettings" && this.settings !== Remote.prototype.settings
          ? create(CommandResultSchema, { kind: { case: "getSettings", value: { settings: await this.settings(eff.machineId, true) } } })
          : eff.sessionId && cmd.case === "getLiveSnapshot"
            ? this.liveSnapshotResult(eff.sessionId)
            : eff.sessionId && cmd.case === "getSharedFileChunk"
              ? create(CommandResultSchema, { kind: { case: "getSharedFileChunk", value: await this.getSharedFileChunk(eff.sessionId, cmd.value.fileId, cmd.value.offset) } })
              : isMachineUpload || !eff.sessionId
                ? await this.machineCommand(eff.machineId, cmd, slot)
                : await this.command(eff.sessionId, cmd, slot);
      const outcome = result.outcome || "executed";
      const reason = result.reason || "completed";
      const norm = create(CommandResultSchema, {
        outcome, reason, conflictKind: result.conflictKind, errorCode: result.errorCode, errorMessage: result.errorMessage, kind: result.kind,
        resultAvailable: result.resultAvailable || outcome === "executed" || (outcome === "conflict" && result.conflictKind === "business"),
      });
      return create(CommandOutcomeEventSchema, { requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.DAEMON, signed: true, hashesMatch: true, attemptBound: true, outerMatchesInner: true, outerOutcome: outcome, outerReason: reason, result: norm });
    } catch (e) {
      if (e instanceof CommandError) {
        if (e.code === "path_changed") return create(CommandOutcomeEventSchema, { requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.TRANSPORT, notForwarded: false, attemptBound: true, transportCode: "path_changed", transportDetail: e.message });
        if (e.fromDaemon) {
          const resultAvailable = e.conflictKind === "identity" ? false : e.resultAvailable;
          return create(CommandOutcomeEventSchema, {
            requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.DAEMON, signed: true, hashesMatch: true, attemptBound: true, outerMatchesInner: true, outerOutcome: e.outcome, outerReason: e.code,
            result: create(CommandResultSchema, { outcome: e.outcome, reason: e.code || "error", conflictKind: e.conflictKind, resultAvailable, errorCode: e.code, errorMessage: e.message }),
          });
        }
        return create(CommandOutcomeEventSchema, { requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.SERVER, notForwarded: e.outcome === "not_sent", attemptBound: true, transportCode: e.code, transportDetail: e.message });
      }
      return create(CommandOutcomeEventSchema, { requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.TRANSPORT, notForwarded: true, transportCode: "unknown", transportDetail: String(e) });
    }
  }

  static create(q: URLSearchParams, pause = sleep, attachmentStore?: AttachmentStore): DemoRemote {
    const r = new DemoRemote(
      {
        server: "https://ra.example.com",
        deviceId: ME,
        accountId: ME,
      },
    );
    if (attachmentStore) {
      Object.defineProperty(r, "attachmentStore", { value: attachmentStore });
    }
    r.opts = q;
    r.pause = pause;
    // ?lang=en shows the English copy, for looking at layouts.
    const lang = q.get("lang");
    if (lang) localeStore.overrideForTesting(lang as Locale);
    r.prepareImageOverride = async (blob, opts) => {
      try {
        const { prepareImageDefault } = await import("../core/attachment-prepare-default");
        return await prepareImageDefault(blob, opts);
      } catch {
        return {
          bytes: new Uint8Array(await blob.arrayBuffer()),
          format: "png" as const,
          width: 1,
          height: 1,
        };
      }
    };
    r.seed();
    r.seedMachineStatus();
    seedDirect(r, q);
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
    if (history) {
      r.demoSessions.get("s-run")!.lastSeq = BigInt(history);
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
    }
    const turns = Math.min(2_000, Math.max(0, Number(q.get("backfill")) || 0));
    if (turns) {
      const all: Event[] = [];
      const add = (kind: EventKind) => all.push(create(EventSchema, { seq: BigInt(all.length + 1), sessionId: "s-run", kind }));
      for (let i = 0; i < turns; i++) {
        add({ case: "userMessage", value: { text: `第 ${i + 1} 轮的问题` } });
        add({ case: "turnStarted", value: {} });
        add({ case: "toolCall", value: { callId: `c${i}`, kind: ToolKind.COMMAND, title: `echo ${i + 1}`, status: "completed" } });
        add({ case: "agentMessage", value: { text: `第 ${i + 1} 轮的回复。` } });
        add({ case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED, durationMs: 2000n } });
      }
      const keep = 10 * 5;
      const tail = all.slice(-keep);
      const delay = Math.min(5000, Math.max(0, Number(q.get("history-delay")) || 0));
      const prefix = q.has("history-prefix");
      const shown = delay ? [] : tail;
      r.demoSessions.get("s-run")!.lastSeq = BigInt(all.length);
      const start = () => {
        if (delay) setTimeout(() => {
          r.backfill.get("s-run")!.shown = prefix ? all.slice(0, keep) : tail;
          r.backfill.get("s-run")!.ready = !prefix;
          r.changed();
        }, delay);
        setTimeout(() => {
          r.backfill.get("s-run")!.shown = all;
          r.backfill.delete("s-run");
          r.changed();
        }, delay + 1500);
      };
      // Like Remote.events: a cold restore starts when the history is read,
      // not while the route's scripts may still be downloading.
      r.backfill.set("s-run", { shown, all, ready: !delay, start: delay ? start : undefined });
      r.evs.set("s-run", all);
      if (!delay) start();
    }
    if (q.has("scheduled-final")) r.seedSchedules();
    if (q.has("shared-files")) r.seedSharedFiles();
    if (q.has("notify")) r.seedNotify();
    if (q.has("one-machine")) for (const id of [...r.demoMachines.keys()]) if (id !== "mac") r.demoMachines.delete(id);
    void r.loadDevices();
    r.syncDemoToCore();
    return r;
  }

  /** Receipts of scheduled pushes (design-system §7 「通知回执」). */
  private seedNotify() {
    const id = "s-notify";
    const now = Date.now();
    const sched = (run: string) => ({ kind: OriginKind.SYSTEM, scheduledTaskId: "agy-health", scheduledRunId: run });
    const reply1 = "最近 24 小时 agy 13 次运行全部拿到了结果，这期间没有出现任务中途的 malformed。";
    const agentText = "2 次中途 malformed，重试都接上了。\n另有 1 次 Hydrogen task 跑了 2 小时 40 分，超过平时的两倍，建议看一眼。";
    const rows: [string, Partial<{ origin: ReturnType<typeof sched> }>, EventKind][] = [
      ["t1", { origin: sched("r1") }, { case: "userMessage", value: { text: "查一下最近 24 小时 agy 的运行情况，有异常才通知我。" } }],
      ["t1", {}, { case: "turnStarted", value: {} }],
      ["t1", {}, { case: "agentMessage", value: { text: reply1 } }],
      ["t1", {}, { case: "tokenUsage", value: { inputTokens: 1_200_000n, outputTokens: 56_000n } }],
      ["t1", {}, { case: "turnEnded", value: { durationMs: 89_000n, outcome: TurnOutcome.COMPLETED } }],
      ["t1", { origin: { kind: OriginKind.SYSTEM } as ReturnType<typeof sched> }, { case: "scheduledNotification", value: { taskId: "agy-health", runId: "r1", text: reply1 } }],
      ["t2", { origin: sched("r2") }, { case: "userMessage", value: { text: "查一下最近 24 小时 agy 的运行情况，有异常才通知我。" } }],
      ["t2", {}, { case: "turnStarted", value: {} }],
      ["t2", {}, { case: "agentMessage", value: { text: "13 次运行里有 2 次中途 malformed，重试都在同一会话接上了，缓存命中。另有 1 次超过 2 小时。" } }],
      ["t2", {}, { case: "scheduledNotification", value: { taskId: "agy-health", runId: "r2", text: agentText } }],
      ["t2", {}, { case: "scheduledNotification", value: { taskId: "agy-health", runId: "r2", text: "@lin 要求把重试次数改成可配置" } }],
      ["t2", {}, { case: "tokenUsage", value: { inputTokens: 1_300_000n, outputTokens: 60_000n } }],
      ["t2", {}, { case: "turnEnded", value: { durationMs: 75_000n, outcome: TurnOutcome.COMPLETED } }],
    ];
    const info = create(SessionSchema, {
      id,
      title: "agy-health",
      cwd: "/Users/me/code/remote-agent",
      agent: Agent.CODEX,
      status: SessionStatus.IDLE,
      mode: ApprovalMode.ASK,
      createdAt: timestampFromMs(now - 60 * min),
    });
    this.demoSessions.set(id, { id, machineId: "mac", info, metaRev: 1n, lastSeq: BigInt(rows.length), updatedMs: BigInt(now), endedMs: 0n });
    this.evs.set(
      id,
      rows.map(([turnId, extra, kind], i) =>
        create(EventSchema, { seq: BigInt(i + 1), sessionId: id, turnId, time: timestampFromMs(now - (rows.length - i) * min), kind, ...extra }),
      ),
    );
  }

  private seedSharedFiles() {
    const mac = this.demoMachines.get("mac");
    if (mac?.meta?.capabilities) {
      mac.meta.capabilities.sharedFiles = true;
      mac.meta.capabilities.sharedFileChunkBytes = 786432;
    }
    const mini = this.demoMachines.get("mini");
    if (mini) {
      mini.meta ??= create(MachineMetaSchema, { hostname: "Mac mini", capabilities: {} });
      mini.meta.capabilities ??= create(MachineMetaSchema, { capabilities: {} }).capabilities!;
      mini.meta.capabilities.sharedFiles = true;
      mini.meta.capabilities.sharedFileChunkBytes = 786432;
    }

    const demoMp4B64 =
      "AAAAIGZ0eXBpc29tAAACAGlzb21pc28yYXZjMW1wNDEAAANpbW9vdgAAAGxtdmhkAAAAAAAAAAAAAAAAAAAD6AAACMoAAQAAAQAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAgAAApR0cmFrAAAAXHRraGQAAAADAAAAAAAAAAAAAAABAAAAAAAACMoAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAABAAAAAAGAAAABAAAAAAAAkZWR0cwAAABxlbHN0AAAAAAAAAAEAAAjKAAAAAAABAAAAAAIMbWRpYQAAACBtZGhkAAAAAAAAAAAAAAAAAABAAAAAkABVxAAAAAAALWhkbHIAAAAAAAAAAHZpZGUAAAAAAAAAAAAAAABWaWRlb0hhbmRsZXIAAAABt21pbmYAAAAUdm1oZAAAAAEAAAAAAAAAAAAAACRkaW5mAAAAHGRyZWYAAAAAAAAAAQAAAAx1cmwgAAAAAQAAAXdzdGJsAAAAt3N0c2QAAAAAAAAAAQAAAKdhdmMxAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAAAAGAAQABIAAAASAAAAAAAAAABFExhdmM2My4xLjEwMiBsaWJ4MjY0AAAAAAAAAAAAAAAAGP//AAAALWF2Y0MBQsAK/+EAFmdCwAraGJsBEAAAAwAQAAADAQDxImoBAARozgRyAAAAEHBhc3AAAAABAAAAAQAAABRidHJ0AAAAAAAALCoAAAAAAAAAGHN0dHMAAAAAAAAAAQAAABIAAAgAAAAAFHN0c3MAAAAAAAAAAQAAAAEAAAAcc3RzYwAAAAAAAAABAAAAAQAAABIAAAABAAAAXHN0c3oAAAAAAAAAAAAAABIAAAL4AAAASQAAAHkAAACRAAAAuAAAANcAAACoAAAAnwAAAEEAAAA7AAAAYgAAAIYAAAD4AAAA7wAAANYAAACHAAAAPQAAAGYAAAAUc3RjbwAAAAAAAAABAAADmQAAAGF1ZHRhAAAAWW1ldGEAAAAAAAAAIWhkbHIAAAAAAAAAAG1kaXJhcHBsAAAAAAAAAAAAAAAALGlsc3QAAAAkqXRvbwAAABxkYXRhAAAAAQAAAABMYXZmNjMuMS4xMDIAAAAIZnJlZQAADHRtZGF0AAACUwYF//9P3EXpvebZSLeWLNgg2SPu73gyNjQgLSBjb3JlIDE2NSByMzIyMiBiMzU2MDVhIC0gSC4yNjQvTVBFRy00IEFWQyBjb2RlYyAtIENvcHlsZWZ0IDIwMDMtMjAyNSAtIGh0dHA6Ly93d3cudmlkZW9sYW4ub3JnL3gyNjQuaHRtbCAtIG9wdGlvbnM6IGNhYmFjPTAgcmVmPTEgZGVibG9jaz0wOjA6MCBhbmFseXNlPTA6MCBtZT1kaWEgc3VibWU9MCBwc3k9MSBwc3lfcmQ9MS4wMDowLjAwIG1peGVkX3JlZj0wIG1lX3JhbmdlPTE2IGNocm9tYV9tZT0xIHRyZWxsaXM9MCA4eDhkY3Q9MCBjcW09MCBkZWFkem9uZT0yMSwxMSBmYXN0X3Bza2lwPTEgY2hyb21hX3FwX29mZnNldD0wIHRocmVhZHM9MiBsb29rYWhlYWRfdGhyZWFkcz0xIHNsaWNlZF90aHJlYWRzPTAgbnI9MCBkZWNpbWF0ZT0xIGludGVybGFjZWQ9MCBibHVyYXlfY29tcGF0PTAgY29uc3RyYWluZWRfaW50cmE9MCBiZnJhbWVzPTAgd2VpZ2h0cD0wIGtleWludD0yNTAga2V5a2V5aW50X21pbj04IHNjZW5lY3V0PTAgaW50cmFfcmVmcmVzaD0wIHJjPWNyZiBtYnRyZWU9MCBjcmY9MzAuMCBxY29tcD0wLjYwIHFwbWluPTAgcXBtYXg9NjkgcXBzdGVwPTQgaXBfcmF0aW89MS40MCBhcT0wAIAAAACdZYiEOhmAccHwABBtrA5AACCNDmA6qqu7uLwagkAhDiCCqqq7u4vBqCQCEHEEFVVV3dxeDUEgEIcQQKqqru7i8GoJAIQcQQVVVXd3F4NQSAQg4ggqqqu7uIcA44PgNWCQ0JAaEkih8cYkkXeJJF/xJIu8pIuZwOIMAjISGhIDQkkX/KTKSpSZSXM4HEGAzISDQkDQkkXeUmUlSkykrAAAAEVBmiBqxAhaO/VHfq/1R36o79X+qO/V8cTR3hERjib/VHeIqjjjid+I4oniK0dxRPCFRzHAHhCtHHxHCFaOPEVHxwfrR3AAAAB1QZpAJrCAhR8cB+o+OA/UfGAKxFR8cAfqPjABXgn1rVVHwSA/BH0kh8cB+o5jj9R8ceJrRxxxN8cTGO+CPpJDmOPE1R36j44/UfHH6j44/UfHeIgo6mzVaMAV4I+qj4wFeCjquq0cfgk6qPjj9aOPwSaqtGg4AAAAjUGaYCqwwCBR8cAH6j44AP1HxwAfqPjgA/UfHAB+eJUGQ9ZvFd/UfHAB+o+OAD9R8cAfqPjgDxNR8cHHEr/6j44AP1HxwB+o+OAP1HxwAccTrHEv8+CjiTgk41POCTgmQkA14KLU84JOGxo8OHhwiEgNeCi00ecPDjU8OCTgyhIBqOJBqmRV44l+xxIr7AAAALRBmoAqsFAIARdJIcAxwACf1HAMcAAn9RwDHAAJ/UcAxwACf1HAMcAAnwiIBFi9RmNAfqOAY4ABP6jgGOAAT+o4BjgAE/qOAY4ABX6jgGOAAV+CLWowMaAA/UcAxwACf1HAMcAAr9RwDHAAJ/UcAxwACf1HAxwACfE1GGNAB+CjieCeYngngkJCEAGsRBRxPBPMTwTwQhIQgANYiCjQzY7MTw2CQkJAAajiVMvHErWOJFMYMfgAAADTQZqgLrBAEFHDHAAK/BLaazY7McMcAAr8EupraaxwxwACfwS2mtprHDHAAK8RBLaa2nuOAGOAAZ+F8Xi8XqIFogCFxeLxXFfqOAY4ABP6jhjgAFfqOGOAAR+o4Y4ABPiajhjgAE+JhfWLqLxcQKRAEXF4vFcV+o4Y4ABP6jgGOAAT+o4Y4ABP6jhjgAE/qOAY4ABP4XxdRdReogdEBFxeJ+K4r8FHNhszYbBgFCQA14KObDZmw2DAoSAGsRBRx5XZmw2DgBggAAuo4nxiool44lTRjgAAAKRBmsAusEAhRwxwACP1HDHAAI/UcAxwACf1HDHAAI/UcMcAAj8L61qouMMaAAR1i/+o4Y4ABP6jgGOAAR+o4BjgAE/qOAY4ABP6jgGOAAR+CfWtajDEQP6jgGOAAT+o4BjgAE+JqOGOAD9RwxwACP1HDHAAI/C+tYvFxgGNAAf3iH+oJAwSAAT+okJCQA16jhjgAE/qOGOAAR+o4Y4ABHHEkUjB+AAAAJtBmuAusEFRwDHAAJ/UcAxwACf1HAMcAAj9RwDHAAJ8TUcMcAHiRAVpJaRMqMxE/xXFeJqOGOAD9RwDHAAK8TUcMcAHiKjhjgAEfqOGOAAR4REBekTK1qSOOLxeK7+o5jgA/UcMcAHiKjhjgAE/qOAY4ABP6jhjgAEeERCyOP1HMcAH6jhjgA8TUcxwAfqOGOAAR+o4Y0AB4REK+AAAAD1BmwA2vWvqS+r/WX1f4JNap16vXV/rf7qv6uqIrRx+tfWuIrRx+HKruxeL/56X7cWxbxE+jj/FsW/V76x8AAAAN0GbIDKxIlffX3199ffBFZmp++vuI/X3199fcIiFHAY4AH6+/9fcR+vvr7hEQo4xwD9criPEeI4AAABeQZtAMrEhEEs+PzV+Pz++CWtU9zvx+fvgl1Hzfj8+OeIglvx+b8fnb4Jb8fnb7fBLHVfnPcfmkXrxv68c/Xjn6tfVvqOO+vqia+4mo4xz9XPgi5WJ1fXjn6++vvrxzgAAAIJBm2AysSGQrEuHud+HuZGHxSeOaab/BLPcPcz3D3MqGAV4JZ7h7me4e5lQwrwSz3D3M9w8cyoYViYVnuPzRCo+OQeJaGAV6Yt/glnuHuZ7h7mMUHgX1439ffX319xFffUcY4fr768c/XjniajjHDxFeOfr76jVX199Tq+vHP145+vsAAAA9EGbgDKwyCkKxIDh4OXng4eDlw84fAUnjmD/x3wiICs8cPHOeOHg5iEGhCAAl5fH+CWeOHjmeOHjmIQaECBFiYVnjh45njh45iEGhAgGXpi3+CqeOHuZ44eOdViEGhCAAIAS8Es8cPHM8cPHMQg0DkAAQAlhASCG95d9RwACMcAAtxFRwBjgAFuIqOAAxwAC3EVHAAY4ABbhCo4AxwACnEiVGMaHiajgAMcAAtxFRwBjgAFPqOAAxwAC31HAAIxwAC31HAAY4ABbxIJr3veMY0DxNRwAGOAAW+o4ADHAALfUcABjgAFvqOAAxwAC31HAAY4ABbAAAADrQZugLrEgpD0SDh45zxw8HO2sjD5SeOZfHfBSEAUz3D3M9w9z1IoGAAKEgAEvBLfh7me4e5iQNCEABLCIgEt+Pzfj8xIBoSAAIBS8Et+PzPcPcxIAaB4AAgELwrfj834/MSAGhgAAgDL002msQEQRVX+4IBK8cPCIhRwACccAAQAPqOAATjgACAN9RwACscAAQBPqOAAVjgACAN4n68cP1HAAJxwABAE+o4ABWOAAIAn1HAAKxwABAE+o4ABOOAAIA3iVT/UcY4fqMAAKocAAQBPqOAAVjgACAJ9RwACscAAQBPqOAAVjgACAJgAAANJBm8AusIgpCsUYo8UYo8ToSCSaa/wUhAEV3xgABNDAABAAWERAIbviQAaGAACAUvBDd8YAAXQkAAQDF6jAADKGAACAUvUYAAXQwAAQCFhAIvWuCASo4McAD9RwADMcAAQCvqOAAVjgACAV9RwACscAAQBvqOAAVjgACAN4niajgDHAAJfUcAAzHAAEAr6jgAFY4AAgFfUcAArHAAEAb6jgAFY4AAgDeJ4io4McAD9RwADMaAAIBS9RwACscAAQCvqOAAVjgACANxFRwACccAAQBsAAAACDQZvgMrEgpBDe/h+4REKOMYABXqMBQwACvBFt2xI0MAAr1GAKGABXqMAUMAArCAlX+o4DHAP1HAY4B+o4DHAA/UYAocAD9RwGOAB8T9RwGOAfqOAxwAP1HAY4B+o4DHAAIfUcBjgAEPEr31HAGOAfqOMcA/UcBjgH6jgMcAD9RwGOABwAAAA5QZoAMrgpWb4JOq9//+IrfiK0ceEK0ceIq/EVfiK0cfq/1o4/V/rf6v9Ucfq/1v9b/V/q/1Rx+qOOAAAAYkGaIDKxIZP9F4v/E1HGOHhCrf+rf4mFr3vfxofiu/1GqHDwjUaocPCMEW9+NCsI1HGOHhmo4xwDwQBFaNAFYIAitHAHhATwgJV+JqOMaB+o4xwD9aOAPCIi+o4xwD9RxjgH";
    const mp4Raw = typeof atob === "function"
      ? Uint8Array.from(atob(demoMp4B64), (c) => c.charCodeAt(0))
      : new Uint8Array([0]);
    const mp4Blob = new Blob([mp4Raw], { type: "video/mp4" });

    const demoSha = new Uint8Array(32).fill(0x5a);
    const demoPngB64 =
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
    const pngRaw = typeof atob === "function"
      ? Uint8Array.from(atob(demoPngB64), (c) => c.charCodeAt(0))
      : new Uint8Array([0x89, 0x50, 0x4e, 0x47]);
    const pngBlob = new Blob([pngRaw], { type: "image/png" });

    const sf = (fileId: string, name: string, sizeBytes: bigint, path: string, sha256: Uint8Array = demoSha) =>
      create(SharedFileSchema, { fileId, name, sizeBytes, path, sha256 });

    const addSession = (
      id: string,
      machineId: string,
      title: string,
      status: SessionStatus,
      events: EventKind[],
    ) => {
      const info = create(SessionSchema, {
        id,
        title,
        cwd: "/Users/me/Movies/daocheng",
        agent: Agent.CLAUDE,
        status,
        mode: ApprovalMode.ASK,
        createdAt: timestampFromMs(Date.now() - 15 * min),
      });
      this.demoSessions.set(id, {
        id,
        machineId,
        info,
        metaRev: 1n,
        lastSeq: BigInt(events.length),
        updatedMs: BigInt(Date.now()),
        endedMs: status === SessionStatus.EXITED ? BigInt(Date.now() - 2 * min) : 0n,
      });
      this.evs.set(
        id,
        events.map((kind, i) =>
          create(EventSchema, {
            seq: BigInt(i + 1),
            sessionId: id,
            time: timestampFromMs(Date.now() - (events.length - i) * 30_000),
            kind,
          }),
        ),
      );
    };

    const sfToolName = "send_file";
    const okStatus = "completed";
    const cUser = "userMessage" as const;
    const cAgent = "agentMessage" as const;
    const cTool = "toolCall" as const;
    const userMsg = (s: string) => ({ case: cUser, value: { text: s } });
    const agentMsg = (s: string) => ({ case: cAgent, value: { text: s } });
    const cmdOk = (callId: string, cmdLine: string) => ({
      case: cTool,
      value: { callId, kind: ToolKind.COMMAND, title: cmdLine, status: okStatus, exitCode: 0 },
    });
    const sendOk = (callId: string, sharedFiles: ReturnType<typeof sf>[]) => ({
      case: cTool,
      value: { callId, kind: ToolKind.OTHER, title: sfToolName, status: okStatus, exitCode: 0, sharedFiles },
    });

    const fChat = sf("sf-chat-1", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    addSession("s-sf-chat", "mac", "daocheng-edit", SessionStatus.IDLE, [
      userMsg("Send the exported final cut to my phone."),
      { case: "turnStarted", value: {} },
      cmdOk("sf-ffmpeg", "ffmpeg -i cut.mov -c:v libx264 out/daocheng-final.mp4"),
      agentMsg("Exported at 1080p (1m 42s)."),
      sendOk("sf-send-1", [fChat]),
      agentMsg("Need a vertical cut too?"),
      { case: "turnEnded", value: { durationMs: 2200n, outcome: TurnOutcome.COMPLETED } },
    ]);

    const sAvail = sf("sf-avail", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    const sDl = sf("sf-dl", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    const sPaused = sf("sf-paused", "assets-backup.zip", 18_874_368n, "/Users/me/Movies/daocheng/assets-backup.zip");
    const sDoneVid = sf("sf-done-vid", "daocheng-final.mp4", BigInt(mp4Raw.length), "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    const sDoneImg = sf("sf-done-img", "cover.png", BigInt(pngRaw.length), "/Users/me/Movies/daocheng/cover.png");
    const sFail = sf("sf-fail", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    const sPdf = sf("sf-pdf", "2026-Q3-roadmap-final-v2.pdf", 4_404_019n, "/Users/me/Documents/2026-Q3-roadmap-final-v2.pdf");
    addSession("s-sf-states", "mac", "shared-file-states", SessionStatus.IDLE, [
      { case: "turnStarted", value: {} },
      sendOk("sf-all-1", [sAvail, sDl, sPaused, sDoneVid, sDoneImg, sPdf, sFail]),
      { case: "turnEnded", value: { durationMs: 1200n, outcome: TurnOutcome.COMPLETED } },
    ]);

    const m1 = sf("sf-m-1", "cover.png", BigInt(pngRaw.length), "/Users/me/Movies/daocheng/cover.png");
    const m2 = sf("sf-m-2", "daocheng-final-vertical-1080-v3.mp4", 64_697_139n, "/Users/me/Movies/daocheng/daocheng-final-vertical-1080-v3.mp4");
    const m3 = sf("sf-m-3", "project.zip", 92_694_118n, "/Users/me/Movies/daocheng/project.zip");
    addSession("s-sf-multi", "mac", "multi-files", SessionStatus.IDLE, [
      userMsg("Send the cover, final cut, and project archive."),
      { case: "turnStarted", value: {} },
      cmdOk("sf-pre-1", "ls -lh out/"),
      cmdOk("sf-pre-2", "zip -r project.zip src/"),
      sendOk("sf-multi", [m1, m2, m3]),
      cmdOk("sf-post-1", "du -sh out/"),
      cmdOk("sf-post-2", "echo done"),
      agentMsg("Here are all three files."),
      { case: "turnEnded", value: { durationMs: 1800n, outcome: TurnOutcome.COMPLETED } },
    ]);

    const fOff = sf("sf-off-1", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    addSession("s-sf-offline", "mini", "offline-files", SessionStatus.IDLE, [
      { case: "turnStarted", value: {} },
      sendOk("sf-off", [fOff]),
      { case: "turnEnded", value: { durationMs: 900n, outcome: TurnOutcome.COMPLETED } },
    ]);

    const fGone = sf("sf-gone-1", "daocheng-final.mp4", 25_480_397n, "/Users/me/Movies/daocheng/out/daocheng-final.mp4");
    addSession("s-sf-ended", "mac", "ended-files", SessionStatus.EXITED, [
      { case: "turnStarted", value: {} },
      sendOk("sf-gone", [fGone]),
      { case: "turnEnded", value: { durationMs: 900n, outcome: TurnOutcome.COMPLETED } },
    ]);

    this.sharedFileStore.seedPartialRaw(
      "s-sf-states",
      "sf-paused",
      JSON.stringify({
        v: SHARED_FILE_META_VERSION,
        sessionId: "s-sf-states",
        fileId: "sf-paused",
        displayName: "assets-backup.zip",
        totalSizeBytes: "18874368",
        chunkBytes: 786432,
        committedOffset: "6291456",
        expectedSha256: bytesToHex(demoSha),
      }),
      new Uint8Array(6_291_456),
    );
    this.sharedFileStore.seedCompleted("s-sf-states", "sf-done-vid", mp4Blob);
    this.sharedFileStore.seedCompleted("s-sf-states", "sf-done-img", pngBlob);
    this.sharedFileStore.seedCompleted("s-sf-multi", "sf-m-1", pngBlob);
  }

  async getSharedFileChunk(sid: string, fileId: string, offset: bigint) {
    await this.pause(240);
    if (this.conn !== "online") {
      const OfflineErr = CommandError;
      throw new OfflineErr("unavailable", "offline");
    }
    const ev = this.evs.get(sid)?.find(
      (e) => e.kind.case === "toolCall" && e.kind.value.sharedFiles.some((f) => f.fileId === fileId),
    );
    const file = ev?.kind.case === "toolCall" ? ev.kind.value.sharedFiles.find((f) => f.fileId === fileId) : undefined;
    const total = file?.sizeBytes ?? 1024n;
    const chunkBytes = 786432;
    const data = new Uint8Array(chunkBytes);
    const end = Math.min(Number(total), Number(offset) + chunkBytes);
    for (let i = Number(offset); i < end; i++) data[i - Number(offset)] = i % 251;
    return { data, totalBytes: total };
  }

  private seedSchedules() {
    const f = structuredClone(scheduledFixture), machine = this.demoMachines.get("mac")!;
    machine.meta!.hostname = f.machineName;
    machine.meta!.capabilities = { ...machine.meta!.capabilities!, scheduledTasks: true };
    this.demoMachines.clear(); this.demoMachines.set("mac", machine);
    const view = this.demoSessions.get("s-idle")!;
    view.id = f.session; view.machineId = "mac";
    view.info = create(SessionSchema, { id:f.session, title:f.sessionTitle, cwd:"/example/project", agent:Agent.CLAUDE, status:SessionStatus.IDLE, mode:ApprovalMode.AUTO });
    this.demoSessions.clear(); this.demoSessions.set(f.session, view);
    this.evs.clear();
    this.evs.set(f.session, [create(EventSchema, {seq:1n,sessionId:f.session,kind:{case:"userMessage",value:{text:"检查 PR 的回复。"}}}),create(EventSchema, {seq:2n,sessionId:f.session,kind:{case:"agentMessage",value:{text:"有变化时会通知你。"}}})]);
    const tasks = f.tasks as DemoScheduleTask[];
    const sample = this.opts.get("schedule-case");
    const longPrompt = "每天早上 8 点前帮我整理项目相关的信息：\n" + Array.from({length: 14}, (_, i) => `${i + 1}. 检查项目中的更新与讨论，保留重要变化的来源；有需要处理的内容就通知我。`).join("\n");
    const quiet = tasks.find(t => t.id === "quiet")!;
    if (sample === "long" || sample === "chat") quiet.spec.message = longPrompt;
    if (sample === "high") quiet.runs = Array.from({length: 38}, (_, i) => ({ ...quiet.runs![0], id: `quiet-${i}`, started_at: new Date(Date.now() - i * 60_000).toISOString(), final_summary: "没有新回复" }));
    if (sample === "crowded") for (let i = 0; i < 15; i++) tasks.push({...structuredClone(quiet), id: `extra-${i}`, spec: {...quiet.spec, title: `检查项目 ${i + 1} 的更新与讨论`}});
    if (sample === "offline") machine.online = false;
    const stopped = tasks.find(t => t.id === "attention")!;
    if (sample === "missing") {
      stopped.spec.target = { type:"resume_session",session:"missing" };
      stopped.status = "failed";
      stopped.reason = "execution_session_missing";
      stopped.runs = stopped.runs?.map(r => ({ ...r, status:"failed", reason:"execution_session_missing", session_id:undefined }));
    }
    if (sample === "failed-once") { stopped.status = "failed"; stopped.reason = "execution_failed"; stopped.spec.trigger = {type:"at",at:new Date().toISOString()}; }
    if (sample === "failed") { stopped.status = "failed"; stopped.reason = "relay_session_limit"; }
    if (sample === "interval-stop") stopped.reason = "agent_schedule_interval_below_minimum";
    if (sample === "uncertain") { stopped.status = "uncertain"; stopped.reason = "restart_dispatching"; }
    if (sample === "chat" || sample === "queue") {
      const origin = { scheduledTaskId: "conversation", scheduledRunId: "demo-run" };
      this.evs.set(f.session, [create(EventSchema, {seq:1n,sessionId:f.session,time:timestampFromMs(Date.now() - min),origin,kind:{case:"userMessage",value:{text:longPrompt}}}), create(EventSchema, {seq:2n,sessionId:f.session,kind:{case:"agentMessage",value:{text:"我会逐项检查，有变化时通知你。"}}}), ...(sample === "queue" ? [create(EventSchema, {seq:3n,sessionId:f.session,time:timestampFromMs(Date.now()),origin,kind:{case:"inputState",value:{requestId:"scheduled-queue",text:"再检查一下讨论中的新回复，有需要处理的变化就通知我。",status:InputStatus.QUEUED,mode:SendMode.QUEUE}}})] : [])]);
    }
    if (sample === "fold") this.evs.set(f.session, [0, 1].flatMap(i => [
      create(EventSchema, {seq:BigInt(i*3+1),sessionId:f.session,time:timestampFromMs(Date.now() - (2-i)*60*min),origin:{scheduledTaskId:"conversation",scheduledRunId:`quiet-${i}`},kind:{case:"userMessage",value:{text:"检查新回复"}}}),
      create(EventSchema, {seq:BigInt(i*3+2),sessionId:f.session,kind:{case:"agentMessage",value:{text:"没有新回复。"}}}),
      create(EventSchema, {seq:BigInt(i*3+3),sessionId:f.session,kind:{case:"turnEnded",value:{durationMs:1000n,outcome:TurnOutcome.COMPLETED,scheduledTaskId:"conversation",scheduledRunId:`quiet-${i}`}}}),
    ]));
    this.demoSchedules.set("mac", sample === "empty" ? [] : tasks);
    this.stored={revision:1n,prefs:create(ProjectPrefsSchema,{schema:1,settings:[{path:"/example/project",machineId:"mac",agent:Agent.CLAUDE,agents:[{agent:Agent.CLAUDE,model:"haiku",mode:ApprovalMode.AUTO}]}]})};
  }

  private opts = new URLSearchParams();
  private demoSwitchConflictFired = false;

  private removedDemoDevices = new Set<string>();
  private demoRecoveryPending = false;
  private demoRecoveryReadyMs = 0n;
  private demoRecoveryStatusStale = false;
  private demoLeaseEndMs = 0n;
  private demoHasProfile = true;
  private demoNeedsRotation = false;
  private demoPendingOp?: ReturnType<typeof create<typeof SecurityPendingOpSummarySchema>>;
  private demoPairings = new Map<string, { code: string; expiresMs: bigint; url: string; approved: boolean }>();

  override get recoveryStatusStale() { return this.demoRecoveryStatusStale; }
  override get securityStatus(): SecurityWorkflowStatus {
    return create(SecurityWorkflowStatusSchema, {
      bootstrap: SecurityBootstrapKind.READY,
      isMember: !this.removedDemoDevices.has(ME),
      isAuthority: this.authorityDeviceId === ME,
      removed: this.removedDemoDevices.has(ME),
      server: this.account.server,
      deviceId: this.account.deviceId,
      accountId: this.account.accountId ?? ME,
      headEpoch: 1n,
      leaseEndMs: this.demoLeaseEndMs,
      leaseValid: this.demoLeaseEndMs > BigInt(Date.now()),
      recoveryPending: this.demoRecoveryPending,
      recoveryReadyMs: this.demoRecoveryReadyMs,
      recoveryStatusStale: this.demoRecoveryStatusStale,
      needsRecoveryCodeRotation: this.demoNeedsRotation,
      hasAccountPublicProfile: this.demoHasProfile,
      hasHandoffOffer: (this.metas.get(ME)?.meta.handoffRecoveryEd?.length ?? 0) === 32,
      pendingOp: this.demoPendingOp,
    });
  }

  private async demoWork() {
    await this.pause(200);
    if (this.opts.get("security") === "fail") throw new Error("没能保存安全操作，请重试");
  }

  override async getSecurityStatus() { return this.securityStatus; }
  override async renewLeaseNow() { await this.demoWork(); this.demoLeaseEndMs = BigInt(Date.now() + 72 * 60 * min); this.changed(); return this.securityStatus; }
  override async prepareRecoveryRotation() { await this.demoWork(); return { recoveryCode: "RA3 DEMO-ONLY-OFFLINE-BACKUP EXAMPLE-NOT-A-RECOVERY-CODE" }; }
  override async confirmRecoveryRotation() { await this.demoWork(); this.demoPendingOp = undefined; this.demoRecoveryPending = false; this.changed(); }
  override async prepareHandoffCode(_storagePersisted: boolean) { await this.demoWork(); return { recoveryCode: "RA3 DEMO-ONLY-OFFLINE-BACKUP EXAMPLE-NOT-A-RECOVERY-CODE" }; }
  override async publishHandoffOffer() { await this.demoWork(); this.demoPendingOp = undefined; this.metas.get(ME)!.meta.handoffRecoveryEd = new Uint8Array(32); await this.loadDevices(); }
  override async discardPendingOp() { await this.demoWork(); this.demoPendingOp = undefined; this.changed(); }
  override async resumeSecurityOperation() { await this.demoWork(); }
  override async completeAccountPublicProfile(_code: string) { await this.demoWork(); this.demoHasProfile = true; this.changed(); }
  override async handoffCandidates(): Promise<SecurityHandoffCandidate[]> {
    return [create(SecurityHandoffCandidateSchema, { deviceId: ANDROID, recoveryEd: new Uint8Array(32) })];
  }
  override async handoffAuthority(id: string) { await this.demoWork(); this.authorityDeviceId = id; this.removedDemoDevices.add(ME); await this.loadDevices(); }
  override async removeDevice(id: string) { await this.demoWork(); this.removedDemoDevices.add(id); await this.loadDevices(); }
  override async deleteMachineRecords(id: string) { await this.demoWork(); this.demoMachines.delete(id); this.syncDemoToCore(); }
  override async startPairing(): Promise<SecurityPairingInfo> {
    await this.demoWork();
    const id = "demo-pair-1";
    const info = { code: "12345678", expiresMs: BigInt(Date.now() + 5 * min), url: "https://app.example.com/#/pair?code=12345678", approved: false };
    this.demoPairings.set(id, info);
    return create(SecurityPairingInfoSchema, { pairingId: id, code: info.code, url: info.url, expiresMs: info.expiresMs });
  }
  override async pollPairing(pairingId: string): Promise<SecurityPairingInfo> {
    const info = this.demoPairings.get(pairingId);
    const compare = this.opts.get("pair") === "compare";
    return create(SecurityPairingInfoSchema, {
      pairingId,
      code: info?.code ?? "12345678",
      url: info?.url ?? "https://app.example.com/#/pair?code=12345678",
      expiresMs: info?.expiresMs ?? BigInt(Date.now() + 5 * min),
      joined: compare,
      approved: !!info?.approved,
      delivered: !!info?.approved,
      joinerKind: DeviceKind.CLIENT,
      sasEmoji: compare ? ["🌲", "🍋", "🚲", "🐳"] : [],
    });
  }
  override async approvePairing(pairingId: string, sasConfirmed: boolean): Promise<SecurityPairingInfo> {
    if (!sasConfirmed) throw new Error("请先确认图案");
    await this.demoWork();
    const info = this.demoPairings.get(pairingId);
    if (info) info.approved = true;
    return this.pollPairing(pairingId);
  }
  override async finishPairingDelivery(pairingId: string): Promise<SecurityPairingInfo> {
    await this.demoWork();
    return this.pollPairing(pairingId);
  }
  override async cancelPairing(pairingId: string): Promise<void> {
    this.demoPairings.delete(pairingId);
  }

  private seedAuthority() {
    this.authorityDeviceId = this.opts.get("role") === "authority" ? ME : ANDROID;
    this.demoRecoveryPending = this.opts.get("recovery") === "pending";
    this.demoRecoveryReadyMs = BigInt(Date.now() + 47 * 60 * min);
    this.demoRecoveryStatusStale = false;
    this.demoLeaseEndMs = BigInt(Date.now() + 48 * 60 * min);
    this.demoHasProfile = this.opts.get("public") !== "missing";
    this.demoNeedsRotation = this.opts.has("rotation");
    if (this.opts.has("archive")) this.removedDemoDevices.add("mini");
  }

  private status!: DemoStatus;
  /** Every machine but the VPS can report its status; ?ms picks how it is (machine-status-demo.ts). */
  private seedMachineStatus() {
    this.status = new DemoStatus(this.opts);
    for (const [id, m] of this.demoMachines) {
      if (id === "vps" || !m.meta) continue;
      m.meta.capabilities ??= create(MachineMetaSchema, { capabilities: {} }).capabilities!;
      m.meta.capabilities.machineStatus = true;
      m.meta.diskStatus = demoDisk(scenarioOf(this.opts, id));
      // The project last used, which 让 Agent 帮我清理 opens its new session in.
      if (id === "mac" && !m.meta.projects.length) m.meta.projects = [create(ProjectSummarySchema, { path: "/Users/me/Code/foo", latestEndedMs: BigInt(now - 3 * min) })];
    }
  }

  private seed() {
    const empty = this.opts.get("empty");
    const machine = (
      id: string,
      hostname: string,
      online: boolean,
      meta: MessageInitShape<typeof MachineMetaSchema> = {},
    ) =>
      this.demoMachines.set(id, {
        id,
        online,
        lastSeenMs: BigInt(online ? now : now - 2 * 60 * min),
        metaRev: 1n,
        meta: create(MachineMetaSchema, {
          hostname,
          home: "/Users/me",
          protocolVersion: PEER_PROTOCOL_VERSION,
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
            observedMs: BigInt(now - min),
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
          {
            agent: Agent.ANTIGRAVITY,
            state: QuotaState.OK,
            plan: "Google AI Pro",
            observedMs: BigInt(now - min),
            windows: [
              {
                name: "gemini-5h",
                usedPercent: 18,
                windowMinutes: 300n,
                resetsAtMs: BigInt(now + 222 * min),
              },
              {
                name: "gemini-weekly",
                usedPercent: 12,
                windowMinutes: 10080n,
                resetsAtMs: BigInt(now + 5 * 24 * 60 * min),
              },
              {
                name: "gemini-argon-wtu-weekly",
                usedPercent: 35,
                windowMinutes: 10080n,
                resetsAtMs: BigInt(now + 4 * 24 * 60 * min),
              },
              {
                name: "3p-5h",
                usedPercent: 40,
                windowMinutes: 300n,
                resetsAtMs: BigInt(now + 135 * min),
              },
              {
                name: "3p-weekly",
                usedPercent: 26,
                windowMinutes: 10080n,
                resetsAtMs: BigInt(now + 6 * 24 * 60 * min),
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
          {
            agent: Agent.ANTIGRAVITY,
            version: "0.2.3",
            verified: true,
            defaultMode: ApprovalMode.ASK,
            defaultModeSource: DefaultModeSource.DAEMON_SETTINGS,
          },
        ],
        capabilities: {
          imageUpload: true,
          fileUpload: this.opts.has("files"),
          maxImagesPerMessage: 6,
          maxUploadBytes: 20n * 1024n * 1024n,
          maxChunkBytes: 786432,
          approvalAlways: true,
          cliSwitches: true,
          usageStats: this.opts.get("usage") !== "old",
        },
        disabledAgents:
          this.opts.get("cliswitches") === "oneleft"
            ? [Agent.CLAUDE, Agent.ANTIGRAVITY]
            : this.opts.get("cliswitches") === "offline" || this.opts.get("cliswitches") === "conflict"
              ? [Agent.CLAUDE]
              : [],
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
          fileUpload: this.opts.has("files"),
            maxImagesPerMessage: 6,
            maxChunkBytes: 786432,
            approvalAlways: true,
          },
        });
        const quotaState = this.opts.get("quota");
        if (quotaState) {
          const meta = this.machines.get("mac")!.meta!;
          meta.quotas = [create(AgentQuotaSchema, {
            agent: Agent.CODEX, plan: "prolite", state: QuotaState.OK,
            observedMs: BigInt(now - (quotaState === "old" ? 14 * 60 * min : min)),
            windows: [{ name: "primary", usedPercent: 29, windowMinutes: 10080n, resetsAtMs: BigInt(now + 6 * 24 * 60 * min) }],
          }), create(AgentQuotaSchema, {
            agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(now - min),
            windows: [{ name: "five_hour", usedPercent: 31, windowMinutes: 300n, resetsAtMs: BigInt(now + 74 * min) },
              { name: "seven_day", usedPercent: quotaState === "warning" ? 97 : 40, windowMinutes: 10080n, resetsAtMs: BigInt(now + 32 * 60 * min) }],
          }), create(AgentQuotaSchema, {
            agent: Agent.ANTIGRAVITY,
            state: quotaState === "unsupported" ? QuotaState.UNSUPPORTED : quotaState === "failed" ? QuotaState.FAILED : QuotaState.WAITING,
            detail: quotaState === "failed" ? t("quota.read_failed") : "",
          })];
        }
        machine("mini", "Mac mini", false);
      }
      const mac = this.demoMachines.get("mac");
      if (mac?.meta) {
        const csMode = this.opts.get("cliswitches");
        const usageMode = this.opts.get("usage");
        if (csMode === "offline" || usageMode === "offline" || usageMode === "offline-none") {
          mac.online = false;
        }
        if (usageMode === "only-unavail") {
          mac.meta.disabledAgents = [Agent.CODEX, Agent.CLAUDE];
        }
        if (usageMode === "offline" && typeof localStorage !== "undefined") {
          try {
            const snap = buildDemoUsageResponse(this.opts, mac.meta.disabledAgents);
            const bytes = toBinary(UsageStatsResponseSchema, snap);
            let bin = "";
            for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
            localStorage.setItem(
              `ra-usage-cache:https://ra.example.com|${ME}:mac:${UsageRangePreset.SEVEN_DAYS}`,
              btoa(bin),
            );
          } catch {
            // ignore storage failure
          }
        } else if (usageMode === "offline-none" && typeof localStorage !== "undefined") {
          try {
            localStorage.removeItem(`ra-usage-cache:https://ra.example.com|${ME}:mac:${UsageRangePreset.TODAY}`);
            localStorage.removeItem(`ra-usage-cache:https://ra.example.com|${ME}:mac:${UsageRangePreset.SEVEN_DAYS}`);
            localStorage.removeItem(`ra-usage-cache:https://ra.example.com|${ME}:mac:${UsageRangePreset.THIRTY_DAYS}`);
          } catch {
            // ignore storage failure
          }
        }
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
      this.demoSessions.set(id, {
        id,
        machineId: f.machine,
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
    if (this.opts.has("question")) {
      const sappr = this.demoSessions.get("s-appr");
      if (sappr?.info) {
        sappr.info.pendingApprovals = 0;
        sappr.info.status = SessionStatus.IDLE;
      }
      const sapprEvs = this.evs.get("s-appr");
      if (sapprEvs) {
        this.evs.set(
          "s-appr",
          sapprEvs.filter(
            (e) =>
              e.kind.case !== "approvalRequested" ||
              e.kind.value.approvalId === "a0",
          ),
        );
      }
      const sask = this.demoSessions.get("s-ask");
      if (sask) {
        sask.updatedMs = BigInt(now - 35 * min);
        if (sask.info) {
          sask.info.createdAt = timestampFromMs(now - 40 * min);
          sask.info.attentionSince = timestampFromMs(now - 40 * min);
        }
      }
      const saskEvs = this.evs.get("s-ask");
      if (saskEvs) {
        for (const e of saskEvs) {
          if (e.kind.case === "questionAsked") {
            e.time = timestampFromMs(now - 35 * min);
            if (e.kind.value.questions.length > 1) {
              e.kind.value.questions = e.kind.value.questions.slice(0, 1);
            }
          }
        }
      }
      if (this.opts.get("question") === "only") {
        const sclaude = this.demoSessions.get("s-claude");
        if (sclaude?.info) {
          sclaude.info.pendingQuestions = 0;
          sclaude.info.status = SessionStatus.IDLE;
        }
        const claudeEvs = this.evs.get("s-claude");
        if (claudeEvs) {
          this.evs.set(
            "s-claude",
            claudeEvs.filter((e) => e.kind.case !== "questionAsked"),
          );
        }
        const splan = this.demoSessions.get("s-plan");
        if (splan?.info) splan.info.planOpen = false;
        const planEvs = this.evs.get("s-plan");
        if (planEvs) {
          this.evs.set(
            "s-plan",
            planEvs.filter((e) => e.kind.case !== "planProposed"),
          );
        }
        const spopup = this.demoSessions.get("s-popup");
        if (spopup?.info) {
          spopup.info.status = SessionStatus.IDLE;
          spopup.info.statusDetail = "";
        }
      }
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
        this.demoSessions.set(id, {
          id,
          machineId: "mac",
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(now - (i + 1) * min),
          endedMs: 0n,
        });
        this.evs.set(id, []);
      }
    if (this.opts.has("long"))
      for (const m of this.demoMachines.values())
        if (m.meta) m.meta.hostname += "-with-a-very-long-machine-name";
    void this.drawPreviews();
    if (this.opts.has("draft"))
      getCoreHost(this).draftSetText(
        sessionDraftScope("s-run"),
        "请检查这段很长的路径 /Users/me/Code/foo/internal/generated/remoteagent/v1/client/session/messages/pending.ts，并补充测试",
      );
    this.stored.prefs = create(ProjectPrefsSchema, {
      schema: 1,
      pinned: [...this.stored.prefs.pinned],
      hidden: [...this.stored.prefs.hidden],
      settings: [
        create(ProjectSettingsSchema, {
          path: "/Users/me/Code/foo",
          machineId: "mac",
          agent: Agent.CODEX,
          updatedMs: BigInt(now),
          agents: [
            create(AgentSettingsSchema, {
              agent: Agent.CODEX,
              model: "gpt-6-astra",
              effort: "medium",
              mode: ApprovalMode.ASK,
              standardContext: false,
            }),
          ],
        }),
      ],
    });
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

  private demoAttempt?: { startedAt: number };
  override get connStep(): string { return this.demoAttempt ? "同步设备状态" : ""; }
  override get connStartedAt(): number | undefined { return this.demoAttempt?.startedAt; }

  override reconnectNow() {
    this.opts.set("conn", "connecting");
    this.demoAttempt = { startedAt: Date.now() };
    this.syncDemoToCore();
  }

  override start() {
    super.start();
    const c = this.opts.get("conn");
    this.demoAttempt = c === "connecting" || c === "reconnecting" ? { startedAt: Date.now() } : undefined;
    void this.loadDevices();
    this.syncDemoToCore();
  }

  override stop() {
    super.stop();
  }

  override retryList() {
    this.demoListRetried = true;
    super.retryList();
    this.syncDemoToCore();
  }

  // ?backfill: the newest events show first, the older ones join above later.
  private backfill = new Map<string, { shown: Event[]; all: Event[]; ready: boolean; start?: () => void }>();
  private demoEvents(sid: string): Event[] {
    const b = this.backfill.get(sid);
    if (b) {
      const start = b.start; b.start = undefined; start?.();
      return b.shown;
    }
    let e = this.evs.get(sid);
    if (!e) this.evs.set(sid, (e = []));
    return e;
  }

  private push(sid: string, kind: EventKind) {
    const list = this.demoEvents(sid);
    const ev = create(EventSchema, {
      seq: BigInt(list.length + 1),
      sessionId: sid,
      time: timestampFromMs(Date.now()),
      kind,
    });
    list.push(ev);
    const s = this.demoSessions.get(sid);
    if (s) {
      s.lastSeq = BigInt(list.length);
      s.updatedMs = BigInt(Date.now());
    }
    if (this.demoStream) {
      this.demoStream.callbacks.onFrame(this.demoStream.legId, {
        case: "eventsAppended",
        value: create(EventsAppendedFrameSchema, {
          sessionId: sid,
          events: [create(PlainEventSchema, { seq: ev.seq, event: ev, verified: true })],
        }),
      });
      if (s) {
        this.demoStream.callbacks.onFrame(this.demoStream.legId, {
          case: "sessionUpdated",
          value: this.plainSession(s),
        });
      }
    }
    this.changed();
  }

  private patch(sid: string, p: Partial<Session>) {
    const s = this.demoSessions.get(sid);
    if (s?.info) {
      s.info = { ...s.info, ...p };
      s.metaRev++;
      if (this.demoStream) {
        this.demoStream.callbacks.onFrame(this.demoStream.legId, {
          case: "sessionUpdated",
          value: this.plainSession(s),
        });
      }
    }
    this.changed();
  }

  private liveSnapshotResult(sid: string): CommandResult {
    const s = this.sessions.get(sid);
    const evs = this.demoEvents(sid);
    const resolved = new Set<string>();
    const answered = new Set<string>();
    let planEv: Event | undefined;
    for (const e of evs) {
      if (e.kind.case === "approvalResolved") resolved.add(e.kind.value.approvalId);
      else if (e.kind.case === "questionAnswered") answered.add(e.kind.value.questionId);
      else if (e.kind.case === "planProposed") planEv = e;
      else if (e.kind.case === "planAnswered") planEv = undefined;
    }
    const actions = evs.filter(
      (e) =>
        (e.kind.case === "approvalRequested" && !resolved.has(e.kind.value.approvalId)) ||
        (e.kind.case === "questionAsked" && !e.kind.value.async && !answered.has(e.kind.value.questionId)),
    );
    const planText = planEv?.kind.case === "planProposed" ? planEv.kind.value.text : "";
    return create(CommandResultSchema, {
      kind: {
        case: "liveSnapshot",
        value: create(LiveSnapshotSchema, {
          session: s?.info,
          running: s?.info?.status === SessionStatus.RUNNING || s?.info?.status === SessionStatus.STARTING,
          actions,
          planEventSeq: planEv?.seq ?? 0n,
          planScreen: s?.info?.planOpen ? planText : "",
          planScreenHash: s?.info?.planOpen ? new Uint8Array(32).fill(1) : new Uint8Array(0),
        }),
      },
    });
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
          throw new CommandError("unknown_outcome", "连接超时", false, false, "unknown");
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
          throw new CommandError("unknown_outcome", "连接超时", false, false, "unknown");
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
        const q = this.demoEvents(sid).find(
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
        const history = this.demoEvents(sid).filter(
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
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(Date.now()),
          endedMs: 0n,
        };
        setTimeout(() => {
          this.demoSessions.set(id, view);
          const list = this.demoEvents(id);
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
          throw new CommandError("unknown_outcome", "Codex 的菜单和预期不一样", false, false, "unknown");
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
      case "getLiveSnapshot":
        return this.liveSnapshotResult(sid);
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
    _slot?: RequestSlot,
  ): Promise<CommandResult> {
    await this.pause(500);
    const cmd = create(CommandSchema, { kind }).kind;
    const m = this.machines.get(machineId);
    if (!m?.online)
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    if (cmd.case === "schedule") {
      const req = cmd.value;
      const tasks = this.demoSchedules.get(machineId) ?? [];
      const encode = (payload: unknown) =>
        create(CommandResultSchema, {
          kind: { case: "schedule", value: { resultJson: new TextEncoder().encode(JSON.stringify(payload)) } },
        });
      if (req.operation === "list") return encode({ tasks });
      const nowIso = new Date().toISOString();
      if (req.operation === "create") {
        const spec = JSON.parse(new TextDecoder().decode(req.specJson));
        const created: DemoScheduleTask = {
          id: "task-" + Math.random().toString(16).slice(2, 8),
          revision: 1,
          status: "scheduled",
          source: "user",
          created_at: nowIso,
          updated_at: nowIso,
          next_run_at: nowIso,
          runs: [],
          spec,
        };
        this.demoSchedules.set(machineId, [created, ...tasks]);
        this.syncDemoToCore();
        return encode(created);
      }
      const existing = tasks.find((t) => t.id === req.taskId);
      if (!existing) throw new CommandError("not_found", "", true);
      if (req.expectedRevision && Number(req.expectedRevision) !== existing.revision) {
        throw new CommandError("conflict", t("errors.the_task_was_just_changed_updated_to_the_latest"), true, false, "conflict", "business");
      }
      const next: DemoScheduleTask = {
        ...existing,
        revision: existing.revision + 1,
        updated_at: nowIso,
      };
      if (req.operation === "update") {
        next.spec = JSON.parse(new TextDecoder().decode(req.specJson));
        next.status = "scheduled";
        next.reason = "";
      } else if (req.operation === "pause") {
        next.status = "paused";
        next.reason = "user_paused";
      } else if (req.operation === "resume") {
        next.status = "scheduled";
        next.reason = "";
      } else if (req.operation === "cancel") {
        next.status = "cancelled";
        next.reason = "";
      } else if (req.operation === "run") {
        next.manual_request = { request_id: "manual-" + Date.now() };
      }
      this.demoSchedules.set(
        machineId,
        tasks.map((t) => (t.id === next.id ? next : t)),
      );
      this.syncDemoToCore();
      return encode(next);
    }
    switch (cmd.case) {
      case "beginImageUpload": {
        const req = cmd.value;
        const up = {
          fileName: req.fileName ?? "",
          totalBytes: req.totalBytes,
          persistedBytes: 0n,
          imageId: "",
          fileId: "",
          expiresMs: BigInt(Date.now() + 86_400_000),
          completed: false,
        };
        this.demoUploads.set(req.transferId, up);
        return create(CommandResultSchema, {
          kind: {
            case: "beginImageUpload",
            value: {
              status: create(ImageUploadStatusSchema, {
                transferId: req.transferId,
                state: ImageUploadState.RECEIVING,
                persistedBytes: 0n,
                totalBytes: req.totalBytes,
                maxChunkBytes: 768 * 1024,
                expiresMs: up.expiresMs,
              }),
            },
          },
        });
      }
      case "putImageChunk": {
        const req = cmd.value;
        const up = this.demoUploads.get(req.transferId);
        if (!up) throw new CommandError("not_found", "upload not found", true);
        const end = req.offset + BigInt(req.data.byteLength);
        if (end > up.persistedBytes) up.persistedBytes = end > up.totalBytes ? up.totalBytes : end;
        return create(CommandResultSchema, {
          kind: {
            case: "putImageChunk",
            value: {
              status: create(ImageUploadStatusSchema, {
                transferId: req.transferId,
                state: ImageUploadState.RECEIVING,
                persistedBytes: up.persistedBytes,
                totalBytes: up.totalBytes,
                maxChunkBytes: 768 * 1024,
                expiresMs: up.expiresMs,
              }),
            },
          },
        });
      }
      case "getImageUploadStatus": {
        const req = cmd.value;
        const up = this.demoUploads.get(req.transferId);
        if (!up) throw new CommandError("not_found", "upload not found", true);
        return create(CommandResultSchema, {
          kind: {
            case: "getImageUploadStatus",
            value: {
              status: create(ImageUploadStatusSchema, {
                transferId: req.transferId,
                state: up.completed ? ImageUploadState.COMPLETE : ImageUploadState.RECEIVING,
                persistedBytes: up.persistedBytes,
                totalBytes: up.totalBytes,
                maxChunkBytes: 768 * 1024,
                imageId: up.imageId,
                fileId: up.fileId,
                expiresMs: up.expiresMs,
              }),
            },
          },
        });
      }
      case "completeImageUpload": {
        const req = cmd.value;
        const up = this.demoUploads.get(req.transferId);
        if (!up) throw new CommandError("not_found", "upload not found", true);
        const id = "up-" + ++this.demoUploadSeq;
        up.completed = true;
        if (up.fileName) up.fileId = "file-" + id;
        else up.imageId = "img-" + id;
        return create(CommandResultSchema, {
          kind: {
            case: "completeImageUpload",
            value: {
              status: create(ImageUploadStatusSchema, {
                transferId: req.transferId,
                state: ImageUploadState.COMPLETE,
                persistedBytes: up.totalBytes,
                totalBytes: up.totalBytes,
                maxChunkBytes: 768 * 1024,
                imageId: up.imageId,
                fileId: up.fileId,
                expiresMs: up.expiresMs,
              }),
            },
          },
        });
      }
      case "abortImageUpload": {
        this.demoUploads.delete(cmd.value.transferId);
        return create(CommandResultSchema, {
          kind: { case: "abortImageUpload", value: {} },
        });
      }
      case "listModels":
        if(this.opts.has("scheduled-final")) return create(CommandResultSchema,{kind:{case:"listModels",value:{defaultModel:"haiku",models:[{slug:"haiku",displayName:"haiku"}]}}});
        if (machineId === "vps") throw new CommandError("unknown_outcome", "连接超时", false, false, "unknown");
        return create(CommandResultSchema, {
          kind: {
            case: "listModels",
            value:
              cmd.value.agent === Agent.CLAUDE
                ? claudeModels
                : cmd.value.agent === Agent.ANTIGRAVITY
                  ? agyModels
                  : models,
          },
        });
      case "getSettings": {
        const st = this.settingsFor();
        st.disabledAgents = [...(m.meta?.disabledAgents ?? [])];
        st.revision = m.meta?.settingsRevision || st.revision || 1n;
        return create(CommandResultSchema, {
          kind: {
            case: "getSettings",
            value: {
              settings: st,
              imageStorage: {
                uploadBytes: 200n << 20n,
                reservedBytes: 12n << 20n,
                protectedBytes: 80n << 20n,
                reclaimableBytes: 120n << 20n,
                cacheBytes: 32n << 20n,
                emergencyReserveBytes: m.meta?.diskStatus?.emergencyReserveBytes ?? 0n,
                sessions: [],
              },
            },
          },
        });
      }
      case "updateSettings": {
        if (cmd.value.settings?.approvalTimeoutSeconds === 3000)
          throw new CommandError("unavailable", `${m.meta?.hostname} 不在线`);
        const cur = this.settingsFor();
        if (cmd.value.toggleAgent !== undefined && cmd.value.toggleAgent !== Agent.UNSPECIFIED) {
          const targetAgent = cmd.value.toggleAgent;
          const enable = !!cmd.value.toggleEnabled;
          const installed = (m.meta?.agents ?? []).map((a) => a.agent);
          if (this.opts.get("cliswitches") === "conflict" && !this.demoSwitchConflictFired) {
            this.demoSwitchConflictFired = true;
            const nextDisabled = [Agent.ANTIGRAVITY];
            cur.disabledAgents = nextDisabled;
            cur.revision = (cur.revision || 1n) + 1n;
            if (m.meta) {
              m.meta.disabledAgents = [...nextDisabled];
              m.meta.settingsRevision = cur.revision;
              this.syncDemoToCore();
            }
            throw new CommandError("settings_revision_conflict", "conflict", true, false, "conflict", "business");
          }
          const nextDisabledSet = new Set<Agent>(m.meta?.disabledAgents ?? cur.disabledAgents);
          if (enable) nextDisabledSet.delete(targetAgent);
          else nextDisabledSet.add(targetAgent);
          if (!enable && installed.filter((a) => !nextDisabledSet.has(a)).length === 0) {
            throw new CommandError("cannot_disable_last_agent", "cannot_disable_last_agent", true, false, "rejected");
          }
          cur.disabledAgents = [...nextDisabledSet];
          cur.revision = (cur.revision || 1n) + 1n;
          if (m.meta) {
            m.meta.disabledAgents = [...cur.disabledAgents];
            m.meta.settingsRevision = cur.revision;
            this.syncDemoToCore();
          }
        } else if (cmd.value.settings) {
          this.saved = create(DaemonSettingsSchema, {
            ...cmd.value.settings,
            disabledAgents: cur.disabledAgents,
            revision: (cur.revision || 1n) + 1n,
          });
        }
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
                emergencyReserveBytes: m.meta?.diskStatus?.emergencyReserveBytes ?? 0n,
                sessions: [],
              },
            },
          },
        });
      }
      case "getUsageStats": {
        const usageMode = this.opts.get("usage");
        if (usageMode === "loading") await this.pause(15_000);
        if (usageMode === "error") {
          throw new CommandError("internal", "usage stats query failed", true, true, "unknown");
        }
        const disabled = m.meta?.disabledAgents ?? [];
        const resp = buildDemoUsageResponse(this.opts, disabled, cmd.value);
        return create(CommandResultSchema, {
          kind: { case: "usageStats", value: resp },
        });
      }
      case "getMachineStatus":
        // ms-slow=<ms>: the answer takes that long, to look at the page before its first one.
        await this.pause(Number(this.opts.get("ms-slow")) || 120);
        return create(CommandResultSchema, {
          kind: { case: "machineStatus", value: this.status.read(machineId, ["s-claude", "s-run", "s-agy"]) },
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
        if (this.opts.has("disk-full")) throw new CommandError("disk_full", "mkdir /Users/me/.remote-agent/sessions: no space left on device", true,false,"executed");
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
          info,
          metaRev: 1n,
          lastSeq: 0n,
          updatedMs: BigInt(Date.now()),
          endedMs: 0n,
        };
        setTimeout(() => {
          this.demoSessions.set(id, view);
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
      idleCloseMinutes: 360,
      defaultModel: "gpt-6-astra",
      defaultEffort: "medium",
      defaultMode: ApprovalMode.AUTO,
      claude: {
        model: "claude-sonnet-5-5",
        effort: "high",
        mode: ApprovalMode.ASK,
      },
      antigravity: {
        model: "gemini-3.1-pro-high",
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
    this.demoDevices.clear();
    for (const m of this.demoMachines.values())
      this.demoDevices.set(m.id, {
        id: m.id,
        kind: DeviceKind.MACHINE,
        createdMs: BigInt(now - 86_400_000),
        metaRev: 0n,
        lastSeenMs: 0n,
      });
    for (const id of [ME, ANDROID, BROWSER]) {
      const m = this.metas.get(id);
      this.demoDevices.set(id, {
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
    for (const id of this.removedDemoDevices) this.demoDevices.delete(id);
    if (this.opts.get("role") === "machine" && this.demoDevices.get(ME)) this.demoDevices.get(ME)!.kind = DeviceKind.MACHINE;
    this.devicesStatus = "ready";
    this.syncDemoToCore();
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

  private stored: { revision: bigint; prefs: ProjectPrefs } = {
    revision: 1n,
    prefs: create(ProjectPrefsSchema, {
      schema: 1,
      pinned: [create(ProjectPrefSchema, { path: "/Users/me/Code/foo", sinceMs: BigInt(now - 86_400_000) })],
      hidden: [
        create(ProjectPrefSchema, { path: "/Users/me/Code/demo", sinceMs: BigInt(now - 86_400_000) }),
        create(ProjectPrefSchema, { path: "/Users/me/work/experiments", sinceMs: BigInt(now - 86_400_000) }),
        create(ProjectPrefSchema, { path: "/Users/me/Code/old-site", sinceMs: BigInt(now - 86_400_000) }),
      ],
    }),
  };

  override async pushKey(): Promise<string> {
    throw new CommandError("unavailable", "服务器没有响应");
  }
}
