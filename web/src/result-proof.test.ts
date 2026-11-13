import { test, expect } from "vitest";
import { fromBinary } from "@bufbuild/protobuf";
import fixture from "../../testdata/transport/proofs.json";
import domain from "../../internal/deviceauth/testdata/domain-v3.json";
import { EnvelopeSchema } from "./gen/remoteagent/v1/envelope_pb";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { openWire, fromEnvelope, parseState, stateHash, requestIdentity } from "./device-security";
import { fromHex, hex } from "./crypto";
import { verifiedOutcome, type Outcome } from "./outcome";
const decode=(s:string)=>Uint8Array.from(atob(s),c=>c.charCodeAt(0));
test("Go signed result proofs and original payload bindings",async()=>{
 const state=await parseState(decode(fixture.State)), genesis=await parseState(decode(domain.genesis.state));
 const pin={account:state.core.account,domain:state.core.domain,genesis:await stateHash(genesis)};
 const open=async (s:string)=>openWire(pin,state,state,fromEnvelope(fromBinary(EnvelopeSchema,decode(s))),fromHex(fixture.Secret),false,0n);
 const request=await open(fixture.Command), binding=await requestIdentity(request.header,request.plaintext);
 expect(hex(binding.key)).toBe(fixture.Key);expect(hex(binding.payload)).toBe(fixture.Payload);
 for(const v of fixture.Vectors){
  let result:Outcome="unknown";
  try{const opened=await open(v.Envelope);result=verifiedOutcome(fromBinary(CommandResultSchema,opened.plaintext),binding.key,binding.payload,v.OuterOutcome,v.OuterReason);}catch{}
  expect(result,v.Name).toBe(v.Expected);
 }
});
