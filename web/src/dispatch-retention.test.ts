import {expect,test} from "vitest";
import {DispatchRegistry,DispatchState} from "./dispatch";
import {RESULT_RETENTION_MS} from "./result-retention.gen";

function fixture() {
 const time={wall:1_000_000,elapsed:0};const callbacks:(()=>void)[]=[];
 const registry=new DispatchRegistry(()=>time,run=>{callbacks.push(run);return ()=>{};});
 return {time,callbacks,registry};
}

test("read-only polling history stays bounded and expires while idle",()=>{
 const {time,callbacks,registry}=fixture();
 for(let i=0;i<2000;i++){
  const state=registry.get(String(i),time.wall+60_000,new DispatchState(false),false);
  const call=state.open();call.delivery().dispatch();call.close();registry.sweep();
  time.wall+=5000;time.elapsed+=5000;
 }
 expect(registry.size).toBeLessThanOrEqual((60_000+RESULT_RETENTION_MS)/5000+1);
 expect(callbacks.length).toBeGreaterThan(0);
 time.wall+=60_000+RESULT_RETENTION_MS;time.elapsed+=60_000+RESULT_RETENTION_MS;
 callbacks.at(-1)!();
 expect(registry.size).toBe(0);
});

test("unresolved histories survive their window and expired active calls",()=>{
 const {time,registry}=fixture(),start=time.wall,deadline=start+60_000;
 const original=registry.get("original",deadline,new DispatchState(false),false);
 const first=original.open();first.delivery().dispatch();first.close();
 time.wall=deadline+RESULT_RETENTION_MS-1;time.elapsed=60_000+RESULT_RETENTION_MS-1;registry.sweep();
 expect(registry.size).toBe(1);
 const copy=registry.get("original",deadline,new DispatchState(false),false);
 expect(copy).toBe(original);
 const active=copy.open(),refusal=active.delivery();refusal.dispatch();refusal.notForwarded();
 expect(active.uncertain).toBe(true);
 time.wall+=2;time.elapsed+=2;registry.sweep();
 expect(registry.size).toBe(1);
 active.close();registry.sweep();
 expect(registry.size).toBe(0);
 expect(active.uncertain).toBe(true);
 // The index owns no payload. A pending slot still owns its evidence even
 // after index cleanup, and restoration continues to require explicit prior.
 time.wall=start;
 expect(registry.get("original",deadline,original,false).unresolved).toBe(true);
 expect(registry.get("restored",deadline,new DispatchState(true),true).unresolved).toBe(true);
});

test("history cleanup requires wall and elapsed time without shortening a known deadline",()=>{
 const {time,registry}=fixture(),start=time.wall,deadline=start+60_000;
 const original=registry.get("original",deadline,new DispatchState(false),false);
 registry.get("original",deadline-30_000,new DispatchState(false),false);
 time.wall=deadline+RESULT_RETENTION_MS+1;registry.sweep();
 expect(registry.size).toBe(1);
 time.wall=start;time.elapsed=60_000+RESULT_RETENTION_MS+1;registry.sweep();
 expect(registry.size).toBe(1);
 time.wall=deadline+RESULT_RETENTION_MS-1;registry.sweep();
 expect(registry.size).toBe(1);
 time.wall+=2;registry.sweep();
 expect(registry.size).toBe(0);
 expect(original.unresolved).toBe(false);
});
