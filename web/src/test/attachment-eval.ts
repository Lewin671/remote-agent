import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import type { AttachmentEvaluator } from "../attach";
import { getCore } from "../core/runtime";
import {
  CacheBootstrapLoadedEventSchema,
  EffectBatchSchema,
  EntropyProvidedSchema,
  EventSchema,
  ScreenSchema,
  StreamReadyEventSchema,
  SyncLifecycleActionSchema,
  SyncLifecycleKind,
  TokenResultEventSchema,
  ViewModelSchema,
  type Effect,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  AttachmentEvalScreenRequestSchema,
  type AttachmentEvalViewModel,
} from "../gen/remoteagent/core/v1/attachments_view_pb";
import {
  DemoMachineSchema,
  DemoSecuritySchema,
  SeedDemoWorldEventSchema,
} from "../gen/remoteagent/core/v1/security_pb";
import { MachineCapabilitiesSchema } from "../gen/remoteagent/v1/capabilities_pb";
import { MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { PEER_PROTOCOL_VERSION } from "../protocol";

export interface TestMachineCaps {
  imageUpload?: boolean;
  fileUpload?: boolean;
  maxImages?: number;
  maxFiles?: number;
  maxFileBytes?: number;
  online?: boolean;
}

/** Lightweight synchronous AttachmentEval backed by the real WASM clientcore for Tray/Attach unit tests. */
export function createTestAttachmentEval(
  machineIds: readonly string[] = ["mac", "vps", "m", "other", "A", "B", "example-computer"],
  initial: TestMachineCaps = {},
): {
  evaluate: AttachmentEvaluator;
  setCaps: (caps: TestMachineCaps) => void;
  close: () => void;
} {
  const core = getCore().newAppCore();
  const baseNow = 1_700_000_000_000n;
  let now = baseNow;
  const dispatch = (event: MessageInitShape<typeof EventSchema>["event"]): Effect[] => {
    now += 10n;
    const raw = core.dispatch(
      toBinary(EventSchema, create(EventSchema, { nowMs: now, monoMs: now - baseNow, event })),
    );
    return fromBinary(EffectBatchSchema, raw).effects;
  };
  const drain = (initialEffects: Effect[]) => {
    const queue = [...initialEffects];
    while (queue.length > 0) {
      const eff = queue.shift()!;
      switch (eff.effect.case) {
        case "cacheRead":
          if (eff.effect.value.kind.case === "bootstrap") {
            queue.push(
              ...dispatch({
                case: "cacheBootstrapLoaded",
                value: create(CacheBootstrapLoadedEventSchema, {
                  opId: eff.effect.value.kind.value.opId,
                }),
              }),
            );
          }
          break;
        case "ensureToken":
          queue.push(
            ...dispatch({
              case: "tokenResult",
              value: create(TokenResultEventSchema, {
                opId: eff.effect.value.opId,
                token: "tok-1",
                expiresMs: baseNow + 86_400_000n,
              }),
            }),
          );
          break;
        case "openStream":
          queue.push(
            ...dispatch({
              case: "streamReady",
              value: create(StreamReadyEventSchema, {
                legId: eff.effect.value.legId,
                serverTimeMs: now,
              }),
            }),
          );
          break;
      }
    }
  };
  dispatch({
    case: "entropyProvided",
    value: create(EntropyProvidedSchema, {
      randomIds: ["r-1", "r-2", "r-3", "r-4", "r-5", "r-6", "r-7", "r-8"],
      unitFloats: [0.5, 0.5, 0.5, 0.5, 0.5, 0.5],
    }),
  });
  drain(
    dispatch({
      case: "syncLifecycle",
      value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.START }),
    }),
  );

  let current: TestMachineCaps = {
    imageUpload: true,
    fileUpload: false,
    maxImages: 6,
    maxFiles: 6,
    maxFileBytes: 100 * 1024 * 1024,
    online: true,
    ...initial,
  };
  const applyCaps = () => {
    dispatch({
      case: "seedDemoWorld",
      value: create(SeedDemoWorldEventSchema, {
        security: create(DemoSecuritySchema, {
          isMember: true,
          selfDeviceId: "dev-test",
          authorityDeviceId: "dev-test",
          leaseEndMs: baseNow + 86_400_000n,
          trustedMachineIds: [...machineIds],
          trustedClientIds: ["dev-test"],
          headEpoch: 1n,
        }),
        machines: machineIds.map((id) =>
          create(DemoMachineSchema, {
            machineId: id,
            online: current.online !== false,
            meta: create(MachineMetaSchema, {
              hostname: id,
              protocolVersion: PEER_PROTOCOL_VERSION,
              capabilities: create(MachineCapabilitiesSchema, {
                imageUpload: current.imageUpload !== false,
                fileUpload: !!current.fileUpload,
                maxImagesPerMessage: current.maxImages ?? 6,
                maxFilesPerMessage: current.maxFiles ?? 6,
                maxFileUploadBytes: BigInt(current.maxFileBytes ?? 100 * 1024 * 1024),
              }),
            }),
          }),
        ),
      }),
    });
  };
  applyCaps();

  return {
    evaluate(req): AttachmentEvalViewModel {
      const screen = create(ScreenSchema, {
        screen: { case: "attachmentEval", value: create(AttachmentEvalScreenRequestSchema, req) },
      });
      const vm = fromBinary(ViewModelSchema, core.view(toBinary(ScreenSchema, screen)));
      if (vm.view.case !== "attachmentEval") throw new Error("unexpected vm: " + vm.view.case);
      return vm.view.value;
    },
    setCaps(patch) {
      current = { ...current, ...patch };
      applyCaps();
    },
    close() {
      core.close();
    },
  };
}
