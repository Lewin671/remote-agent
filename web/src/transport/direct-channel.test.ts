import { expect, test, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { FragmentSchema } from "../gen/remoteagent/v1/transport_pb";
import { DirectBudget, DirectChannel } from "./direct-channel";
import { fakeRTC } from "../test/peer/fake-rtc";

test("a rejected asynchronous reassembly hash releases its closing owner's budget",async()=>{
 const budget=new DirectBudget(),sdk=fakeRTC();
 const channel=new DirectChannel({peer:"peer",connection:new Uint8Array(16),use:1,generation:1n,deadline:Date.now()+10000,budget,valid:()=>true,stun:[],factory:sdk.factory,candidate:()=>{},state:()=>{},frame:()=>{},closed:()=>{}});
 const raw=toBinary(FragmentSchema,create(FragmentSchema,{messageId:new Uint8Array(16),index:0,count:1,ciphertextLength:1,ciphertextSha256:new Uint8Array(32),pathGeneration:1n,fragment:new Uint8Array([1])}));
 let reject!:(error:Error)=>void;const pending=new Promise<ArrayBuffer>((_,r)=>reject=r);
 const digest=vi.spyOn(crypto.subtle,"digest").mockReturnValue(pending);
 try {
  const received=(channel as unknown as {receive(raw:Uint8Array<ArrayBuffer>):Promise<void>}).receive(raw);
  channel.close();reject(Error("fixture digest failure"));await expect(received).rejects.toThrow("fixture digest failure");
  expect(budget.ownerAvailable("peer","replacement")).toBe(true);
  expect(budget.acquire("peer","replacement",1)).toBe(true);budget.release("peer",1);
 } finally {digest.mockRestore();channel.close();}
});

test("a throwing close observer cannot retain the SDK reservation",()=>{
 const budget=new DirectBudget(),sdk=fakeRTC();
 const channel=new DirectChannel({peer:"peer",connection:new Uint8Array(16),use:1,generation:1n,deadline:Date.now()+10000,budget,valid:()=>true,stun:[],factory:sdk.factory,candidate:()=>{},state:()=>{},frame:()=>{},closed:()=>{throw Error("fixture close observer")}});
 expect(()=>channel.close()).toThrow("fixture close observer");expect(budget.ownerAvailable("peer","replacement")).toBe(true);
 expect(()=>channel.close()).not.toThrow();
});
