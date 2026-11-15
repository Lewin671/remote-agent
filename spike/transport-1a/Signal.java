package example.transportlab;
import java.util.*;
import java.nio.*;
import java.nio.charset.*;

// Native JVM/Android counterpart of the experimental signal receiver.
public final class Signal {
 public static final class Action {long cls,type,generation;byte[] payload;Action(long c,long t,byte[] p,long g){cls=c;type=t;payload=p;generation=g;}}
 final Wire.Identity self,peer;final byte[] connection;final long use;
 long generation=1,next=1,gapDeadline;boolean bootReady,localSDP,remoteSDP,dtls,confirmed,closed,retired,ended;
 Wire.Opened challenge;
 byte[] localHash={},remoteHash={},localFingerprint={},remoteFingerprint={},remoteUfrag={},remoteMid={};
 int candidates,batches,sdkActions,duplicates,conflicts,rejected,unsupported,staleChallenges;
 final Map<String,byte[]> seen=new HashMap<>();final Map<String,Long> cacheUntil=new HashMap<>();final Map<Long,Wire.Opened> pending=new HashMap<>();final Map<Long,Long> pendingAt=new HashMap<>();
 public Signal(Wire.Identity self,Wire.Identity peer,byte[] connection,long use){this.self=self;this.peer=new Wire.Identity(peer);this.connection=connection;this.use=use;}
 synchronized void rememberChallenge(Wire.Opened sent){Wire.Context c=sent.ctx;Wire.check(sent.type==2&&c.cls==1&&Arrays.equals(c.domain,Wire.utf8("bootstrap"))&&c.generation==0&&c.use==0&&c.seq!=0&&sent.hash.length==32&&Arrays.equals(c.from,self.id)&&Arrays.equals(c.to,peer.id)&&Arrays.equals(c.connection,connection)&&Arrays.equals(c.senderBoot,self.boot)&&Arrays.equals(c.receiverBoot,peer.boot)&&Arrays.equals(c.senderInstance,self.instance)&&Arrays.equals(c.receiverInstance,peer.instance));challenge=new Wire.Opened();challenge.ctx=Wire.context(c.encode());challenge.hash=sent.hash.clone();challenge.type=2;}
 void acceptACK(Wire.Opened ack){byte[] raw=ack.payload;Map<Integer,Wire.Field> f=Wire.parse(raw,5);Wire.check(f.size()==5);for(int n:new int[]{4,5,7,8})Wire.check(f.containsKey(n)&&!f.get(n).bytes);Wire.check(f.containsKey(6)&&f.get(6).bytes&&f.get(6).b.length==32&&f.get(4).u>=1&&f.get(4).u<=2&&f.get(5).u!=0&&Long.compareUnsigned(f.get(8).u,2)<=0);
  if(challenge==null){Wire.check(f.get(7).u!=0||(f.get(4).u==1&&f.get(8).u==0));return;}
  if(f.get(7).u==0||(challenge!=null&&Arrays.equals(f.get(6).b,challenge.hash))){Wire.check(challenge!=null);Wire.Context c=challenge.ctx,a=ack.ctx;Wire.check(Arrays.equals(a.from,c.to)&&Arrays.equals(a.to,c.from)&&Arrays.equals(a.connection,c.connection)&&Arrays.equals(a.senderBoot,c.receiverBoot)&&Arrays.equals(a.receiverBoot,c.senderBoot)&&Arrays.equals(a.senderInstance,c.receiverInstance)&&Arrays.equals(a.receiverInstance,c.senderInstance));Wire.check(f.get(4).u==c.cls&&f.get(5).u==c.seq&&Arrays.equals(f.get(6).b,challenge.hash)&&f.get(7).u==c.generation&&f.get(8).u==c.use);bootReady=true;}else Wire.check(bootReady);
 }
 boolean offerer(){return Wire.compare(self.id,peer.id)<0;}
 boolean ready(){return !closed&&localSDP&&remoteSDP&&dtls&&confirmed;}
 static String key(Wire.Context c){return Wire.hex(c.from)+"/"+Wire.hex(c.senderInstance)+"/"+Wire.hex(c.connection)+"/"+Long.toUnsignedString(c.use)+"/"+Long.toUnsignedString(c.generation)+"/"+c.cls+"/"+Long.toUnsignedString(c.seq);}
 static String text(byte[] b)throws Exception{return StandardCharsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(b)).toString();}
 static byte[][] info(byte[] raw)throws Exception{SDPCheck.validate(raw);byte[] uf={},mid={},fp={};for(String line:text(raw).split("\r\n")){if(line.startsWith("a=ice-ufrag:"))uf=Wire.utf8(line.substring(12));if(line.startsWith("a=mid:"))mid=Wire.utf8(line.substring(6));if(line.startsWith("a=fingerprint:sha-256 "))fp=Wire.unhex(line.substring("a=fingerprint:sha-256 ".length()).replace(":",""));}return new byte[][]{uf,mid,fp};}
 synchronized void setLocal(byte[] raw,boolean offer,long gen)throws Exception{Wire.check(gen==generation&&!closed&&bootReady&&offer==offerer()&&(!offer||!localSDP)&&(offer||remoteSDP));byte[][] meta=info(raw);localSDP=true;localHash=Wire.hash(raw);localFingerprint=meta[2];}
 synchronized void nextGeneration(long gen){Wire.check((!closed||retired)&&gen==generation+1&&gen!=0);generation=gen;next=1;closed=retired=false;localSDP=remoteSDP=dtls=confirmed=ended=false;pending.clear();pendingAt.clear();gapDeadline=0;candidates=batches=0;}
 static byte[] sdpBody(byte[] raw){return Wire.pb(Wire.b(1,raw),Wire.b(2,Wire.hash(raw)));}
 void remember(Wire.Opened o){String k=key(o.ctx);seen.put(k,o.hash);cacheUntil.put(k,o.ctx.deadline+300000);}
 synchronized List<Action> receive(byte[] raw,long now)throws Exception{try{return accept(Wire.open(raw,peer,self,now),now);}catch(Exception e){rejected++;throw e;}}
 synchronized List<Action> accept(Wire.Opened o,long now)throws Exception{
  Wire.Context c=o.ctx;Wire.check(Arrays.equals(c.connection,connection)&&Arrays.equals(c.from,peer.id));String k=key(c);
  if(seen.containsKey(k)){if(!Arrays.equals(seen.get(k),o.hash)){conflicts++;throw new IllegalArgumentException("conflict");}duplicates++;return Collections.emptyList();}Wire.check(seen.size()<2048);
  if(c.cls==2){if(o.type==4)acceptACK(o);remember(o);if(o.type!=4&&o.type!=10){unsupported++;return Arrays.asList(new Action(2,10,Wire.pb(Wire.u(1,c.cls),Wire.u(2,o.type),Wire.u(3,c.seq),Wire.b(4,o.hash)),c.generation));}return Arrays.asList(new Action(2,o.type,o.payload,c.generation));}
  if(o.type==1||o.type==2){Wire.check(Arrays.equals(c.domain,Wire.utf8("bootstrap"))&&c.generation==0&&c.use==0);Map<Integer,Wire.Field> f=Wire.parse(o.payload,5);Wire.need(f,o.type==1?"bbb":f.size()==5?"bbbbu":"bbbb");Wire.check(o.type!=2||f.size()!=5||Long.compareUnsigned(f.get(5).u,1)<=0);Wire.check(Arrays.equals(f.get(1).b,c.connection)&&Arrays.equals(f.get(2).b,c.senderInstance)&&Arrays.equals(f.get(3).b,c.senderBoot)&&(o.type!=2||Arrays.equals(f.get(4).b,self.boot)));if(!localSDP&&!remoteSDP){peer.instance=c.senderInstance.clone();peer.boot=c.senderBoot.clone();}remember(o);if(o.type==2){bootReady=true;if(f.containsKey(5)&&f.get(5).u==1)staleChallenges++;}return Arrays.asList(new Action(1,o.type,o.payload,0));}
  if(!bootReady&&!closed&&!offerer()&&o.type==3&&c.cls==1&&Arrays.equals(c.domain,Wire.utf8("signal"))&&c.use==use&&c.generation==generation&&c.seq==1&&Arrays.equals(c.receiverBoot,self.boot)&&Arrays.equals(c.senderBoot,peer.boot)&&Arrays.equals(c.receiverInstance,self.instance)&&Arrays.equals(c.senderInstance,peer.instance)){Map<Integer,Wire.Field> f=Wire.parse(o.payload,2);Wire.need(f,"bb");Wire.check(Arrays.equals(Wire.hash(f.get(1).b),f.get(2).b));info(f.get(1).b);bootReady=true;}
  Wire.check((!closed||(retired&&c.generation==generation+1))&&bootReady&&c.use==use&&Long.compareUnsigned(c.generation,generation)>=0&&Long.compareUnsigned(c.generation,generation+1)<=0);
  if(c.generation==generation+1){Wire.check(!offerer());nextGeneration(c.generation);}
  Wire.check(Long.compareUnsigned(c.seq,next)>=0);if(Long.compareUnsigned(c.seq,next)>0){Wire.check(pending.size()<32);remember(o);pending.put(c.seq,o);pendingAt.put(c.seq,now);refreshGap();return Collections.emptyList();}
  List<Action> actions=new ArrayList<>();for(;;){remember(o);Action action=apply(o);next++;if(action.type!=0)actions.add(action);if(!pending.containsKey(next))break;o=pending.remove(next);pendingAt.remove(next);}refreshGap();return actions;
 }
 Action apply(Wire.Opened o)throws Exception{
  Action action=new Action(1,o.type,o.payload,o.ctx.generation);
  switch((int)o.type){
   case 3:case 4:{Wire.check(o.type==3?(!offerer()&&!remoteSDP):(offerer()&&localSDP&&!remoteSDP));Map<Integer,Wire.Field> f=Wire.parse(o.payload,2);Wire.need(f,"bb");Wire.check(Arrays.equals(Wire.hash(f.get(1).b),f.get(2).b));byte[][] m=info(f.get(1).b);remoteSDP=true;remoteHash=f.get(2).b;remoteUfrag=m[0];remoteMid=m[1];remoteFingerprint=m[2];sdkActions++;action.payload=f.get(1).b;break;}
   case 5:{Wire.check(remoteSDP&&!ended);Wire.Reader r=new Wire.Reader(o.payload);int count=0;while(r.at<o.payload.length&&o.payload[r.at]==10){r.at++;long size=r.uint();Wire.check(size>=0&&size<=1024&&size<=o.payload.length-r.at&&count<16);Map<Integer,Wire.Field> f=Wire.parse(Arrays.copyOfRange(o.payload,r.at,r.at+(int)size),5);r.at+=(int)size;Wire.need(f,"bbbuu");String original=text(f.get(1).b);Wire.check(original.startsWith("candidate:")&&!original.contains("\0")&&!original.contains("\r")&&!original.contains("\n"));String[] tokens=original.split("\\s+");for(int i=0;i<tokens.length;i++)if(tokens[i].equals("ufrag"))Wire.check(i+1<tokens.length&&tokens[i+1].equals(text(f.get(2).b)));Wire.check(Arrays.equals(f.get(2).b,remoteUfrag)&&Arrays.equals(f.get(3).b,remoteMid)&&f.get(4).u==0&&f.get(5).u==generation);count++;}Map<Integer,Wire.Field> tail=Wire.parse(Arrays.copyOfRange(o.payload,r.at,o.payload.length),1);Wire.check(tail.size()==1&&tail.containsKey(2)&&!tail.get(2).bytes&&tail.get(2).u==batches&&count>0&&batches<4&&candidates+count<=64);candidates+=count;batches++;sdkActions+=count;break;}
   case 6:{Wire.check(remoteSDP&&!ended);Map<Integer,Wire.Field> f=Wire.parse(o.payload,3);Wire.need(f,"buu");Wire.check(Arrays.equals(f.get(1).b,remoteUfrag)&&f.get(2).u==candidates&&f.get(3).u==batches);sdkActions++;ended=true;break;}
   case 7:Wire.check(localSDP&&remoteSDP&&Arrays.equals(o.payload,confirmBody()));confirmed=true;break;
   case 8:Wire.check(offerer());break;
   case 9:closed=true;pending.clear();pendingAt.clear();gapDeadline=0;break;
   default:unsupported++;action=new Action(2,10,Wire.pb(Wire.u(1,o.ctx.cls),Wire.u(2,o.type),Wire.u(3,o.ctx.seq),Wire.b(4,o.hash)),o.ctx.generation);
  }return action;
 }
 byte[] confirmBody(){return Wire.pb(Wire.b(1,offerer()?localHash:remoteHash),Wire.b(2,offerer()?remoteHash:localHash),Wire.b(3,offerer()?localFingerprint:remoteFingerprint),Wire.b(4,offerer()?remoteFingerprint:localFingerprint));}
 void refreshGap(){gapDeadline=0;for(long at:pendingAt.values()){long due=at+2500;if(gapDeadline==0||Long.compareUnsigned(due,gapDeadline)<0)gapDeadline=due;}}
 synchronized boolean tick(long now){for(String k:new ArrayList<>(cacheUntil.keySet()))if(now>cacheUntil.get(k)){cacheUntil.remove(k);seen.remove(k);}if(gapDeadline!=0&&now>=gapDeadline){closed=retired=true;pending.clear();pendingAt.clear();gapDeadline=0;return true;}return false;}
}
