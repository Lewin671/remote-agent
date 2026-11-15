package io.github.lewin671.remoteagent.core.peer;

import java.security.SecureRandom;
import java.util.*;
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters;
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters;

/** Mobile protocol endpoint: authenticated refusal, never a WebRTC executor. */
public final class PeerResponder {
 private final byte[] seed,instance=new byte[16],boot=new byte[32];
 private String head="";
 private final Map<String,Session> sessions=new HashMap<>();
 private static final class Seen {byte[] hash,reply;long until;Seen(byte[] h,byte[] r,long u){hash=h;reply=r;until=u;}}
 private static final class Session { Wire.Identity peer;long seq,until;Map<String,Seen> seen=new HashMap<>();Session(Wire.Identity p,long u){peer=p;until=u;} }
 public PeerResponder(byte[] seed) {this.seed=seed.clone();SecureRandom random=new SecureRandom();random.nextBytes(instance);random.nextBytes(boot);}
 public synchronized void clear(){sessions.clear();head="";}
 public static byte[] body(byte[] raw) {
  if(raw.length<2||raw.length>65540||raw[0]!=90)return null;
  try{Wire.Reader r=new Wire.Reader(raw);r.at=1;long n=r.uint();if(n>65536||n!=raw.length-r.at)return null;return Arrays.copyOfRange(raw,r.at,raw.length);}catch(Exception e){return null;}
 }
 public synchronized byte[] receive(byte[] raw,String account,byte[] domain,byte[] state,long epoch,String selfID,byte[] xPrivate,String peerID,byte[] ed,byte[] x,long now,long validUntil) {
  try {
   if(now>=validUntil||raw.length>65536)return null;
   String binding=account+Wire.hex(domain)+Wire.hex(state);if(!head.equals(binding)){sessions.clear();head=binding;}
   sessions.entrySet().removeIf(e->now>e.getValue().until);
   Wire codec=new Wire(Wire.utf8(account),domain,state,epoch);
   Wire.Identity self=new Wire.Identity(Wire.utf8(selfID),1,instance,boot,new Ed25519PrivateKeyParameters(seed,0).generatePublicKey().getEncoded(),new X25519PrivateKeyParameters(xPrivate,0).generatePublicKey().getEncoded(),seed,xPrivate);
   Wire.Identity member=new Wire.Identity(Wire.utf8(peerID),2,new byte[0],new byte[0],ed,x,null,null);
   Wire.Opened o=null;Session session=null;String key=null;
   try{o=codec.open(raw,member,self,now);}catch(Exception ignored){}
   if(o!=null&&o.ctx.cls==1&&o.type==1){
    Schema.known(1,1,o.payload);Map<Integer,Wire.Field> body=Schema.fields(o.payload,"Probe");
    Wire.check(Arrays.equals(body.get(1).b,o.ctx.connection)&&Arrays.equals(body.get(2).b,o.ctx.senderInstance)&&Arrays.equals(body.get(3).b,o.ctx.senderBoot));
    key=peerID+Wire.hex(o.ctx.connection);session=sessions.get(key);
    if(session==null){if(sessions.size()>=128||sessions.values().stream().filter(s->Arrays.equals(s.peer.id,member.id)).count()>=32)return null;member.instance=o.ctx.senderInstance;member.boot=o.ctx.senderBoot;session=new Session(member,o.ctx.deadline+300000);sessions.put(key,session);}
   }else{
    o=null;
    for(Map.Entry<String,Session> e:sessions.entrySet()){if(!Arrays.equals(e.getValue().peer.id,member.id))continue;try{Wire.Opened candidate=codec.open(raw,e.getValue().peer,self,now);if(e.getKey().equals(peerID+Wire.hex(candidate.ctx.connection))){o=candidate;session=e.getValue();key=e.getKey();break;}}catch(Exception ignored){}}
   }
   if(o==null||session==null)return null;
   Schema.known(o.ctx.cls,o.type,o.payload);
   if(o.ctx.cls==2&&(o.type==1||o.type==14))capabilityExpiry(o,now);
   session.seen.entrySet().removeIf(e->now>e.getValue().until);
   String slot=o.ctx.use+"/"+o.ctx.generation+"/"+o.ctx.cls+"/"+o.ctx.seq;
   Seen old=session.seen.get(slot);if(old!=null)return Arrays.equals(old.hash,o.hash)?old.reply:null;
   if(session.seen.size()>=2048||sessions.values().stream().mapToInt(s->s.seen.size()).sum()>=8192)return null;
   byte[] reply=null;
   if(o.ctx.cls==1&&o.type==1){
    Wire.Context c=new Wire.Context(self,session.peer,o.ctx.connection,0,0,1,now,1,true);c.deadline=Math.min(c.deadline,o.ctx.deadline);
    reply=codec.seal(self,session.peer,c,2,Wire.pb(Wire.b(1,c.connection),Wire.b(2,instance),Wire.b(3,boot),Wire.b(4,session.peer.boot),Wire.b(6,o.hash)),null,null,null);
   }else if(!(o.ctx.cls==2&&(o.type==4||o.type==10||o.type==11||o.type==12||o.type==13)) && !(o.ctx.cls==1&&o.type==2)){
    Wire.Context c=new Wire.Context(self,session.peer,o.ctx.connection,0,1,++session.seq,now,2,false);c.deadline=Math.min(c.deadline,o.ctx.deadline);
    reply=codec.seal(self,session.peer,c,10,Wire.pb(Wire.u(1,o.ctx.cls),Wire.u(2,o.type),Wire.u(3,o.ctx.seq),Wire.b(4,o.hash)),null,null,null);
   }
   byte[] output=null;if(reply!=null){Map<Integer,Wire.Field> f=Wire.parse(reply,5);output=Wire.pb(Wire.b(3,Wire.pb(Wire.b(1,f.get(2).b),Wire.u(2,f.get(3).u),Wire.b(3,f.get(4).b),Wire.b(4,f.get(5).b))));}
   session.until=Math.max(session.until,o.ctx.deadline+300000);session.seen.put(slot,new Seen(o.hash,output,o.ctx.deadline+300000));return output;
  }catch(Exception ignored){return null;}
 }
 static void capabilityExpiry(Wire.Opened o,long now){Map<Integer,Wire.Field> f=Schema.fields(o.payload,o.type==14?"CapabilitySnapshot":"Capabilities");long expires=f.get(o.type==14?3:4).u;Wire.check(expires>now&&expires>o.ctx.issued&&expires-o.ctx.issued<=60000);}
}
