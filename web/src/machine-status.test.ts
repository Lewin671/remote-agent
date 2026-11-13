import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { MachineStatusSchema, ResourceUsageSchema, DiskStatusSchema } from "./gen/remoteagent/v1/local_pb";
import { MachineMetaSchema, MachineCapabilitiesSchema, ProjectSummarySchema } from "./gen/remoteagent/v1/remote_pb";
import { MachineStatusController, stableUsage, machineDiskReminder, recentMachineProject, isDiskFull } from "./machine-status";
afterEach(()=>vi.useRealTimers());
it("polls immediately, retains values after three failures, recovers and stops when hidden", async()=>{
 vi.useFakeTimers(); const sample=create(MachineStatusSchema,{observedMs:1n}); let calls=0;
 const p=new MachineStatusController(async()=>{calls++;if(calls>=2&&calls<=4)throw Error();return {...sample,observedMs:BigInt(calls)}});
 p.setActive(true,true,false);await vi.advanceTimersByTimeAsync(4000);expect(calls).toBe(0);
 p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(0);expect(p.state).toBe("live");
 await vi.advanceTimersByTimeAsync(6000);expect(p.state).toBe("stale");expect(p.last?.observedMs).toBe(1n);
 await vi.advanceTimersByTimeAsync(2000);expect(p.state).toBe("live");expect(p.curveSinceMs).toBe(5n);
 p.setActive(false,true,true);await vi.advanceTimersByTimeAsync(10000);expect(calls).toBe(5);p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(0);expect(calls).toBe(6);expect(p.curveSinceMs).toBe(5n);p.dispose();
});
it("never overlaps requests and discards a late hidden result",async()=>{
 vi.useFakeTimers();let resolve!: (s:ReturnType<typeof create<typeof MachineStatusSchema>>) =>void;
 const fetch=vi.fn(()=>new Promise<ReturnType<typeof create<typeof MachineStatusSchema>>>(r=>resolve=r));
 const p=new MachineStatusController(fetch);p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(10000);expect(fetch).toHaveBeenCalledTimes(1);
 p.setActive(false,true,true);p.setActive(true,true,true);resolve(create(MachineStatusSchema));await vi.advanceTimersByTimeAsync(0);expect(p.last).toBeUndefined();expect(fetch).toHaveBeenCalledTimes(2);p.dispose();
});
it("reports first-load failure and preserves strict ranking thresholds",async()=>{
 vi.useFakeTimers();const p=new MachineStatusController(async()=>{throw Error()});p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(4000);expect(p.state).toBe("unavailable");p.dispose();
 const a=create(ResourceUsageSchema,{id:"a",cpuPercent:10,memoryBytes:100_000_000n}), b=create(ResourceUsageSchema,{id:"b",cpuPercent:12,memoryBytes:150_000_000n});
 for(const memory of [true,false])expect(stableUsage(["a","b"],[b,a],memory)[0].id).toBe("a");
 b.cpuPercent+=.01;b.memoryBytes++;for(const memory of [true,false])expect(stableUsage(["a","b"],[a,b],memory)[0].id).toBe("b");
});
it("uses capability, online status, recent projects and an explicit error code",()=>{
 const meta=create(MachineMetaSchema,{capabilities:create(MachineCapabilitiesSchema,{machineStatus:true}),diskStatus:create(DiskStatusSchema,{availableBytes:9_000_000_000n}),projects:[create(ProjectSummarySchema,{path:"old",latestCreatedMs:1n}),create(ProjectSummarySchema,{path:"new",latestEndedMs:2n})]});
 expect(machineDiskReminder(false,meta)).toBeUndefined();expect(machineDiskReminder(true,meta)).toBeDefined();expect(recentMachineProject(meta)).toBe("new");expect(isDiskFull({code:"disk_full"})).toBe(true);expect(isDiskFull(Error("no space left on device"))).toBe(false);
});
it("keeps last values offline and bounds each request to fifteen seconds",async()=>{
 vi.useFakeTimers();let hung=false,calls=0;
 const p=new MachineStatusController(async(signal)=>{calls++;if(!hung)return create(MachineStatusSchema,{observedMs:1n});return await new Promise((_,reject)=>signal.addEventListener("abort",()=>reject(Error())));});
 p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(0);p.setActive(true,false,true);expect(p.state).toBe("stale");expect(p.last?.observedMs).toBe(1n);await vi.advanceTimersByTimeAsync(20000);expect(calls).toBe(1);
 hung=true;p.setActive(true,true,true);await vi.advanceTimersByTimeAsync(45000);expect(p.failures).toBe(3);expect(p.state).toBe("stale");p.dispose();
});
it("moves challengers through multiple rows and drops missing identities",()=>{
 const rows=[...["a","b","new","c"].map((id,i)=>create(ResourceUsageSchema,{id,cpuPercent:[5,10,30,25][i]}))];
 expect(stableUsage(["gone","a","b","c"],rows).map(r=>r.id)).toEqual(["new","c","b","a"]);
});
