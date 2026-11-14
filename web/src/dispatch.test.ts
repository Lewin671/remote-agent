import {expect,test} from "vitest";
import rows from "../../testdata/transport/dispatch.json";
import {DispatchState,type DispatchCall} from "./dispatch";

test.each(rows.filter(row => !process.env.RA_DISPATCH_CASE || process.env.RA_DISPATCH_CASE===row.name))("dispatch ownership $name",row=>{
 const state=new DispatchState(row.prior);
 const calls=new Map<string,DispatchCall>();
 const deliveries=new Map<string,ReturnType<DispatchCall["delivery"]>>();
 for(const step of row.steps){
  switch(step.op){
   case "open":calls.set(step.call!,state.open());break;
   case "delivery":deliveries.set(step.delivery!,calls.get(step.call!)!.delivery());break;
   case "dispatch":deliveries.get(step.delivery!)!.dispatch();break;
   case "unsent":deliveries.get(step.delivery!)!.notForwarded();break;
   case "close":calls.get(step.call!)!.close();break;
   case "check":expect(calls.get(step.call!)!.uncertain).toBe(step.uncertain);expect(calls.get(step.call!)!.hasPrior).toBe(step.hasPrior);break;
   default:throw Error(step.op);
  }
 }
});
