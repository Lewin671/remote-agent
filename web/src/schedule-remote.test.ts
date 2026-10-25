import { create } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { CommandError, Remote } from "./remote";
import {
  MachineMetaSchema,
  CommandResultSchema,
} from "./gen/remoteagent/v1/remote_pb";
import { task } from "./test/schedule-fixture";
import { PROTOCOL_VERSION } from "./protocol";

class Probe extends Remote {
  constructor() {
    super(
      { server: "https://relay.example.com", deviceId: "test" } as any,
      undefined,
    );
  }
}
function probe() {
  const r = new Probe();
  r.conn = "online";
  r.machines.set("computer", {
    id: "computer",
    online: true,
    lastSeenMs: 0n,
    metaRev: 1n,
    meta: create(MachineMetaSchema, {
      protocolVersion: PROTOCOL_VERSION,
      capabilities: { scheduledTasks: true },
      scheduleRevision: 1n,
    }),
  });
  return r;
}
const result = (value: unknown) =>
  create(CommandResultSchema, {
    kind: {
      case: "schedule",
      value: { resultJson: new TextEncoder().encode(JSON.stringify(value)) },
    },
  });
it("refreshes on capabilities, revision and reconnect without polling", async () => {
  const r = probe();
  const send = vi
    .spyOn(r, "machineCommand")
    .mockResolvedValue(result({ tasks: [task()] }));
  r.syncSchedules();
  await vi.waitFor(() =>
    expect(r.schedules.get("computer")?.loading).toBe(false),
  );
  for (let i = 0; i < 5; i++) r.syncSchedules();
  expect(send).toHaveBeenCalledTimes(1);
  r.machines.get("computer")!.meta!.scheduleRevision++;
  r.syncSchedules();
  await vi.waitFor(() => expect(send).toHaveBeenCalledTimes(2));
  r.conn = "offline";
  r.syncSchedules();
  expect(r.taskEntries()).toHaveLength(1);
  r.conn = "online";
  r.syncSchedules();
  await vi.waitFor(() => expect(send).toHaveBeenCalledTimes(3));
  r.machines.get("computer")!.meta!.capabilities!.scheduledTasks = false;
  expect(r.taskEntries()).toHaveLength(0);
  r.syncSchedules();
  expect(send).toHaveBeenCalledTimes(3);
});
it("follows a stale in-flight list with the new revision and retains a failed snapshot", async () => {
  const r = probe();
  let finish!: (r: ReturnType<typeof result>) => void;
  const send = vi
    .spyOn(r, "machineCommand")
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValue(result({ tasks: [task({ revision: 2 })] }));
  r.syncSchedules();
  r.machines.get("computer")!.meta!.scheduleRevision = 2n;
  r.syncSchedules();
  finish(result({ tasks: [task()] }));
  await vi.waitFor(() =>
    expect(r.schedules.get("computer")?.tasks[0].revision).toBe(2),
  );
  expect(send).toHaveBeenCalledTimes(2);
  send.mockRejectedValue(new Error("offline"));
  await r.refreshSchedules("computer");
  expect(r.schedules.get("computer")?.tasks[0].revision).toBe(2);
  expect(r.schedules.get("computer")?.error).toBe("offline");
  r.syncSchedules();
  expect(send).toHaveBeenCalledTimes(3);
});
it("retries create with its frozen request and uses CAS for all task mutations", async () => {
  const r = probe();
  const send = vi
    .spyOn(r, "machineCommand")
    .mockRejectedValueOnce(new CommandError("unknown", "lost reply"));
  await expect(
    r.changeSchedule("computer", "draft", {
      operation: "create",
      spec: task().spec,
      requestId: "stable",
    }),
  ).rejects.toThrow("lost reply");
  const first = send.mock.calls[0];
  send
    .mockResolvedValueOnce(result(task()))
    .mockResolvedValue(result({ tasks: [task()] }));
  await r.changeSchedule("computer", "draft", {
    operation: "create",
    spec: { ...task().spec, message: "changed draft" },
    requestId: "another",
  });
  expect(send.mock.calls[1]).toEqual(first);
  send.mockRejectedValueOnce(new CommandError("aborted", "changed", true));
  await expect(
    r.changeSchedule("computer", "task-1", {
      operation: "pause",
      taskId: "task-1",
      revision: 8,
    }),
  ).rejects.toThrow("任务刚被修改，已更新为最新内容");
  expect(r.scheduleAttempts.get("computer/task-1")?.frozen).toBe(false);
  expect(
    send.mock.calls.find(
      ([, q]) => q?.case === "schedule" && q.value.operation === "pause",
    )?.[1],
  ).toMatchObject({ value: { expectedRevision: 8n } });
  r.machines.get("computer")!.online = false;
  await expect(
    r.changeSchedule("computer", "task-1", { operation: "cancel" }),
  ).rejects.toThrow("不在线");
});

it("stores only the verified encrypted list and restores it as an offline snapshot", async () => {
  const { SyntheticRemote } = await import("./test/remote-fixture");
  const { parseHeader } = await import("./device-security");
  const { toBinary } = await import("@bufbuild/protobuf");
  const { ListMachinesResponseSchema, ListRemoteSessionsResponseSchema } =
    await import("./gen/remoteagent/v1/relay_pb");
  const { PayloadKind } = await import("./gen/remoteagent/v1/envelope_pb");
  const r = await SyntheticRemote.create();
  const stored = new Map<string, unknown>();
  const cache = {
    get: async (key: string) => stored.get(key),
    set: async (key: string, value: unknown) => {
      stored.set(key, value);
    },
  };
  (r as any).cache = cache;
  const meta = create(MachineMetaSchema, {
    protocolVersion: PROTOCOL_VERSION,
    scheduleRevision: 9n,
    capabilities: { scheduledTasks: true },
  });
  r.conn = "online";
  r.machines.set(r.machine, {
    id: r.machine,
    online: true,
    lastSeenMs: 0n,
    metaRev: 1n,
    meta,
  });
  const machineRow = {
    id: r.machine,
    online: true,
    meta: await r.seal(
      { kind: PayloadKind.MACHINE_META, seq: 1n },
      MachineMetaSchema,
      meta,
    ),
  };
  (r as any).api = {
    forward: async ({ command }: any) => ({
      result: await r.seal(
        {
          kind: PayloadKind.COMMAND_RESULT,
          requestId: parseHeader(command.securityHeader).request,
        },
        CommandResultSchema,
        result({ tasks: [task()] }),
      ),
    }),
  };
  let other: InstanceType<typeof SyntheticRemote> | undefined;
  try {
    await r.refreshSchedules(r.machine);
    expect(r.schedules.get(r.machine)?.error).toBe("");
    expect(r.schedules.get(r.machine)?.tasks).toHaveLength(1);
    const kept = stored.get("schedules:" + r.machine) as { bytes: Uint8Array };
    expect(kept.bytes).toBeInstanceOf(Uint8Array);
    expect(new TextDecoder().decode(kept.bytes)).not.toContain(
      task().spec.message,
    );
    const identity = { deviceId: r.account.deviceId, server: r.account.server };
    stored.set("machines", {
      ...identity,
      bytes: toBinary(
        ListMachinesResponseSchema,
        create(ListMachinesResponseSchema, { machines: [machineRow] }),
      ),
    });
    stored.set("sessions", {
      ...identity,
      bytes: toBinary(
        ListRemoteSessionsResponseSchema,
        create(ListRemoteSessionsResponseSchema),
      ),
    });
    other = new SyntheticRemote(r.world, r.member, r.seed);
    (other as any).cache = cache;
    await (other as any).warm();
    expect(other.machines.get(r.machine)?.online).toBe(false);
    expect(other.taskEntries().map((e) => e.task.spec.message)).toEqual([
      task().spec.message,
    ]);
    // A snapshot is never mistaken for a current revision or a live machine.
    expect(other.schedules.get(r.machine)?.revision).toBe(0n);
  } finally {
    other?.stop();
    await r.dispose();
  }
}, 20000);

it("combines task lists from supported computers while retaining an offline computer's snapshot", async () => {
  const r = probe();
  r.machines.set("second", { ...r.machines.get("computer")!, id: "second", meta: create(MachineMetaSchema, { protocolVersion: PROTOCOL_VERSION, capabilities: { scheduledTasks: true }, scheduleRevision: 2n }) });
  vi.spyOn(r, "machineCommand").mockImplementation(async machineId => result({ tasks: [task({ id: machineId + "-task" })] }));
  r.syncSchedules();
  await vi.waitFor(() => expect(r.taskEntries()).toHaveLength(2));
  expect(r.taskEntries().map(e => e.machineId).sort()).toEqual(["computer", "second"]);
  r.machines.get("second")!.online = false;
  r.syncSchedules();
  expect(r.taskEntries()).toHaveLength(2);
  r.machines.get("second")!.meta!.capabilities!.scheduledTasks = false;
  expect(r.taskEntries().map(e => e.machineId)).toEqual(["computer"]);
});

it("fetches unloaded task target sessions by ID", async()=>{
 const r=probe();const t=task();t.spec.target={type:"resume_session",session:"missing-target"};
 vi.spyOn(r,"machineCommand").mockResolvedValue(result({tasks:[t]}));const fetch=vi.spyOn(r,"ensureSession").mockResolvedValue();
 await r.refreshSchedules("computer");expect(fetch).toHaveBeenCalledExactlyOnceWith("missing-target");
});
it("deletes a removed computer's encrypted task snapshot and rejects a late list",async()=>{
 const r=probe();const stored=new Map<string,unknown>([["schedules:computer",new Uint8Array([1])]]);
 (r as any).cache={set:async(k:string,v:unknown)=>{if(v===undefined)stored.delete(k);else stored.set(k,v);}};
 r.schedules.set("computer",{tasks:[task()],revision:1n,error:"",loading:false});
 let finish!:(value:ReturnType<typeof result>)=>void;vi.spyOn(r,"machineCommand").mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));
 const pending=r.refreshSchedules("computer");r.machines.delete("computer");r.syncSchedules();finish(result({tasks:[task()]}));await pending;
 expect(r.schedules.has("computer")).toBe(false);expect(stored.has("schedules:computer")).toBe(false);
});

it("does not hold the list slot or revision follow-up while resolving a target",async()=>{
 const r=probe();const t=task();t.spec.target={type:"resume_session",session:"target"};
 let release!:()=>void;const waiting=new Promise<void>(resolve=>{release=resolve;});
 vi.spyOn(r,"ensureSession").mockReturnValue(waiting);
 const send=vi.spyOn(r,"machineCommand").mockResolvedValue(result({tasks:[t]}));
 let settled=false;const read=r.refreshSchedules("computer").then(()=>{settled=true;});
 await vi.waitFor(()=>expect(settled).toBe(true));
 r.machines.get("computer")!.meta!.scheduleRevision++;
 await r.refreshSchedules("computer");expect(send).toHaveBeenCalledTimes(2);release();await read;
});

it("retains an uncertain relay abort instead of treating it as a task revision conflict",async()=>{
 const r=probe();vi.spyOn(r,"machineCommand").mockRejectedValue(new CommandError("aborted","relay outcome unknown",false));
 await expect(r.changeSchedule("computer","task-1",{operation:"pause",taskId:"task-1",revision:1})).rejects.toThrow("relay outcome unknown");
 expect(r.scheduleAttempts.get("computer/task-1")?.frozen).toBe(true);
});
it("does not restore a revoked archive after cached decryption suspends",async()=>{
  const r=probe();let finish!: (v:unknown)=>void;
  vi.spyOn(r as any,"openSecureMessage").mockImplementation(()=>new Promise(resolve=>{finish=resolve}));
  const {toBinary}=await import("@bufbuild/protobuf");const {EnvelopeSchema}=await import("./gen/remoteagent/v1/envelope_pb");
  const restoring=(r as any).restoreScheduleSnapshot("computer",{bytes:toBinary(EnvelopeSchema,create(EnvelopeSchema)),requestId:"read"});
  (r as any).archiveMachines.add("computer");r.machines.get("computer")!.online=false;r.syncSchedules();
  finish({msg:result({tasks:[task()]})});await restoring;
  expect(r.schedules.has("computer")).toBe(false);expect(r.taskEntries()).toEqual([]);
});
it("removes a snapshot whose cache write completes after revocation",async()=>{
  const r=probe();const stored=new Map<string,unknown>();let finish!:()=>void;
  (r as any).cache={set:async(k:string,v:unknown)=>{if(v)await new Promise<void>(resolve=>{finish=resolve});stored.set(k,v)}};
  const writing=(r as any).keepScheduleSnapshot("computer",{bytes:new Uint8Array([1]),requestId:"read"});
  (r as any).archiveMachines.add("computer");r.machines.get("computer")!.online=false;r.syncSchedules();
  finish();await writing;expect(stored.get("schedules:computer")).toBeUndefined();
});
