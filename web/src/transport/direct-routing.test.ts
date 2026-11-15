import { expect, test } from "vitest";
import { DirectRouting } from "./direct-routing";
import { peerFrameBody } from "./peer-frame";
import { b, pb } from "./peer/wire";

test("bulk upgrade requires two 4 MiB comparisons of the same file, retains relay samples and falls back", () => {
 let now=0;const route=new DirectRouting(()=>now),file={kind:"bulk",file:"one"} as const;
 for(let round=0;round<2;round++) {
  expect(route.choose(file,true)).toBe("relay");route.observe(file,"relay",4<<20,400);
  expect(route.choose(file,true)).toBe("direct");route.observe(file,"direct",4<<20,410);now+=5000;
  if(round===0)expect(route.choose(file,true)).toBe("relay");
 }
 const selected=Array.from({length:8},()=>route.choose(file,true));expect(selected.filter(x=>x==="relay")).toHaveLength(1);
 expect(route.choose({kind:"bulk",file:"different"},true)).toBe("relay");
 for(let window=0;window<2;window++) {route.observe(file,"relay",4<<20,400);route.observe(file,"direct",4<<20,800);now+=5000;route.choose(file,true);}
 expect(route.choose(file,true)).toBe("relay");now+=4999;expect(route.choose(file,true)).toBe("relay");
 route.fail(file);expect(route.filePath).toBe("relay");expect(route.choose(file,false)).toBe("relay");
});
test("slow direct samples never upgrade and physical failure immediately retains relay",()=>{
 let now=0;const route=new DirectRouting(()=>now),file={kind:"bulk",file:"one"} as const;
 for(let i=0;i<3;i++){route.observe(file,"relay",4<<20,100);route.observe(file,"direct",4<<20,200);now+=5000;}
 expect(route.choose(file,true)).toBe("relay");route.fail();expect(route.choose(file,true)).toBe("relay");
});
test("the original maximum-sized delivery survives its ServerFrame wrapper",()=>{
 const body=new Uint8Array(65536);const raw=pb(b(11,body));expect(peerFrameBody(raw,11)).toEqual(body);
 expect(peerFrameBody(pb(b(11,new Uint8Array(65537))),11)).toBeUndefined();expect(peerFrameBody(raw,12)).toBeUndefined();
 expect(peerFrameBody(new Uint8Array([...raw,0]),11)).toBeUndefined();
});

test("requires two actual 4 MiB comparisons from the same file before bulk upgrade", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file-a" } as const;
  expect(routing.choose(hint, true)).toBe("relay");
  for (let round = 0; round < 2; round++) {
    routing.observe(hint, "relay", 4 << 20, 1000); expect(routing.choose(hint, true)).toBe("direct");
    routing.observe(hint, "direct", 4 << 20, 1000 / .96); now += 2000;
    expect(routing.choose(hint, true)).toBe(round ? "direct" : "relay");
  }
  expect(routing.choose({ kind: "bulk", file: "file-b" }, true)).toBe("relay");
  expect(routing.choose(hint, false)).toBe("relay");
});
test("does not replace throughput with RTT and holds five seconds after a hard fault", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file" } as const;
  routing.observe(hint, "relay", 4 << 20, 1000); routing.observe(hint, "direct", 4 << 20, 2000);
  expect(routing.choose(hint, true)).toBe("relay");
  routing.fail(hint); routing.observe(hint, "relay", 4 << 20, 1000);
  now = 4999; expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; expect(routing.choose(hint, true)).toBe("direct");
});
test("keeps read preference behind duration and sample requirements", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 100; i++) { now = i * 50; routing.observe(hint, "relay", 1, 150); routing.observe(hint, "direct", 1, 40); }
  expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; routing.observe(hint, "direct", 1, 40);
  expect(routing.choose(hint, true)).toBe("direct"); expect(routing.choose(hint, true)).toBe("direct");
});
