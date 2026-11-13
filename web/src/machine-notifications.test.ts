import { create } from "@bufbuild/protobuf";
import { expect,it,vi } from "vitest";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { NotifyContentSchema } from "./gen/remoteagent/v1/remote_pb";
import { DiskStatusSchema,ResourcePressure } from "./gen/remoteagent/v1/local_pb";
import { machineDiskNotice,machineNotificationKinds } from "./machine-notifications";
import { Remote } from "./remote";
it.each([false,true])("negotiates disk push without breaking old subscriptions (capability=%s)",async advertised=>{
 const base=[NotifyKind.ATTENTION,NotifyKind.TURN_ENDED,NotifyKind.SECURITY,NotifyKind.SCHEDULED];
 const received: NotifyKind[][]=[];
 const api={getPushKey:vi.fn(async()=>advertised?{publicKey:"fixture",machineDiskNotifications:true}:{publicKey:"old-server"}),
 setPushSubscription:vi.fn(async(q:{kinds:NotifyKind[]})=>{if(!advertised&&q.kinds.some(k=>k>4)) throw Error("unknown notification kind");received.push(q.kinds);})};
 const remote:Remote=Object.assign(Object.create(Remote.prototype),{api});
 await remote.setPushSubscription("fixture",[...base,NotifyKind.MACHINE_DISK]);
 expect(received).toEqual([advertised?[...base,NotifyKind.MACHINE_DISK]:base]);
 expect(machineNotificationKinds(base,false)).toEqual(base);
 await remote.setPushSubscription("",[]);expect(received.at(-1)).toEqual([]);
});
it("renders data-only notices locally in both languages and scopes identity to a machine",()=>{
 const c=create(NotifyContentSchema,{kind:NotifyKind.MACHINE_DISK,machineId:"computer-a",machineName:"测试电脑",diskStatus:create(DiskStatusSchema,{availableBytes:1_500_000_000n,pressure:ResourcePressure.CRITICAL})});
 expect(machineDiskNotice(c,"zh-CN")).toEqual({machineId:"computer-a",tag:"computer-a/NOTIFY_KIND_MACHINE_DISK",title:"测试电脑 磁盘快满了，只剩 1.5 GB",body:"现在清理，否则会话可能启动不了。"});
 expect(machineDiskNotice(c,"en")?.body).toBe("Clean up now or sessions may not start.");
 const b={...c,machineId:"computer-b"};expect(machineDiskNotice(b)?.tag).not.toBe(machineDiskNotice(c)?.tag);
 expect(machineDiskNotice({...c,title:"daemon text"})).toBeUndefined();
 c.diskStatus!.availableBytes=50_000_000_000n;expect(machineDiskNotice(c)).toBeUndefined();
});
