package io.github.lewin671.remoteagent.core.peer;

import java.io.*;
import java.nio.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.security.*;
import java.security.spec.*;
import java.util.*;
import javax.crypto.*;
import javax.crypto.spec.*;
import org.bouncycastle.jce.provider.BouncyCastleProvider;
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters;
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters;

// Authenticated protocol 16 codec. Scope is immutable and belongs to one verified head.
public final class Wire {
  static final Provider BC=new BouncyCastleProvider();
  final byte[] ACCOUNT, ACCOUNT_DOMAIN, HEAD; final long epoch;
  public Wire(byte[] account, byte[] domain, byte[] head, long epoch) { this.ACCOUNT=account.clone();this.ACCOUNT_DOMAIN=domain.clone();this.HEAD=head.clone();this.epoch=epoch; }
  public static byte[] utf8(String s){return s.getBytes(StandardCharsets.UTF_8);}
  public static String text(byte[] b){try{return StandardCharsets.UTF_8.newDecoder().onMalformedInput(java.nio.charset.CodingErrorAction.REPORT).onUnmappableCharacter(java.nio.charset.CodingErrorAction.REPORT).decode(ByteBuffer.wrap(b)).toString();}catch(Exception e){throw new IllegalArgumentException("invalid utf8",e);}}
  public static byte[] cat(byte[]...parts){ByteArrayOutputStream out=new ByteArrayOutputStream();for(byte[] b:parts)out.write(b,0,b.length);return out.toByteArray();}
  public static boolean equal(byte[] a,byte[] b){return Arrays.equals(a,b);}
  public static byte[] hash(byte[] b){try{return MessageDigest.getInstance("SHA-256").digest(b);}catch(Exception e){throw new IllegalStateException(e);}}
  public static String hex(byte[] b){StringBuilder s=new StringBuilder();for(byte v:b)s.append(String.format(java.util.Locale.ROOT,"%02x",v&255));return s.toString();}
  public static byte[] unhex(String s){byte[] b=new byte[s.length()/2];for(int i=0;i<b.length;i++)b[i]=(byte)Integer.parseInt(s.substring(2*i,2*i+2),16);return b;}
  public static byte[] num(long n){return ByteBuffer.allocate(8).putLong(n).array();}
  public static byte[] lp(byte[]...parts){ByteArrayOutputStream out=new ByteArrayOutputStream();for(byte[] b:parts){byte[] n=ByteBuffer.allocate(4).putInt(b.length).array();out.write(n,0,4);out.write(b,0,b.length);}return out.toByteArray();}
  public static byte[] vint(long v){ByteArrayOutputStream out=new ByteArrayOutputStream();do{int b=(int)(v&127);v>>>=7;if(v!=0)b|=128;out.write(b);}while(v!=0);return out.toByteArray();}
  public static final class Field {public int n;public long u;public byte[] b;public boolean bytes;Field(int n,long u){this.n=n;this.u=u;}Field(int n,byte[] b){this.n=n;this.b=b;bytes=true;}}
  public static Field b(int n,byte[] v){return new Field(n,v);}
  public static Field u(int n,long v){return new Field(n,v);}
  public static byte[] getBytes(Map<Integer,Field> f,int n){Field v=f.get(n);check(v!=null&&v.bytes);return v.b;}
  public static long getUint(Map<Integer,Field> f,int n){Field v=f.get(n);check(v!=null&&!v.bytes);return v.u;}
  public static byte[] pb(Field...fields){Arrays.sort(fields,Comparator.comparingInt(f->f.n));ByteArrayOutputStream out=new ByteArrayOutputStream();for(Field f:fields){byte[] raw=cat(vint(((long)f.n<<3)|(f.bytes?2:0)),f.bytes?cat(vint(f.b.length),f.b):vint(f.u));out.write(raw,0,raw.length);}return out.toByteArray();}
  public static final class Reader {public byte[] data;public int at;public Reader(byte[] d){data=d;}public long uint(){long n=0;int start=at;for(int i=0;i<10;i++){check(at<data.length);int b=data[at++]&255;check(i!=9||b<=1);n|=(long)(b&127)<<(i*7);if((b&128)==0){check(Arrays.equals(Arrays.copyOfRange(data,start,at),vint(n)));return n;}}throw new IllegalArgumentException("integer");}}
  public static void check(boolean ok){if(!ok)throw new IllegalArgumentException("invalid peer packet");}
  public static Map<Integer,Field> parse(byte[] raw,int max){check(raw.length<=65536);Map<Integer,Field> out=new HashMap<>();Reader r=new Reader(raw);int last=0;while(r.at<raw.length){long tag=r.uint();int n=(int)(tag>>>3),wire=(int)(tag&7);check(Long.compareUnsigned(tag>>>3,4095)<=0&&n>last&&out.size()<max);last=n;if(wire==0)out.put(n,u(n,r.uint()));else{check(wire==2);long size=r.uint();check(size>=0&&size<=raw.length-r.at);out.put(n,b(n,Arrays.copyOfRange(raw,r.at,r.at+(int)size)));r.at+=(int)size;}}return out;}
  public static void need(Map<Integer,Field> f,String schema){check(f.size()==schema.length());for(int i=0;i<schema.length();i++)check(f.containsKey(i+1)&&f.get(i+1).bytes==(schema.charAt(i)=='b'));}
  static byte[] kdf(byte[] secret,byte[] info)throws Exception{Mac m=Mac.getInstance("HmacSHA256");m.init(new SecretKeySpec(hash(utf8("remote-agent/peer-hkdf/v1")),"HmacSHA256"));byte[] prk=m.doFinal(secret);m.init(new SecretKeySpec(prk,"HmacSHA256"));return m.doFinal(cat(info,new byte[]{1}));}
  static byte[] gcm(byte[] key,byte[] nonce,byte[] aad,byte[] body,boolean seal)throws Exception{check(nonce.length==12);Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(seal?Cipher.ENCRYPT_MODE:Cipher.DECRYPT_MODE,new SecretKeySpec(key,"AES"),new GCMParameterSpec(128,nonce));c.updateAAD(aad);return c.doFinal(body);}
  public static final class Identity {
    public byte[] id,instance,boot;public long role;PrivateKey sign,box;PublicKey signPub,boxPub;
    public Identity(byte[] id,long role,byte[] instance,byte[] boot,byte[] edPublic,byte[] xPublic,byte[] seed,byte[] xPrivate)throws Exception {
      this.id=id.clone();this.role=role;this.instance=instance.clone();this.boot=boot.clone();
      KeyFactory sk=KeyFactory.getInstance("Ed25519",BC),xk=KeyFactory.getInstance("X25519",BC);
      signPub=sk.generatePublic(new X509EncodedKeySpec(cat(unhex("302a300506032b6570032100"),edPublic)));
      boxPub=xk.generatePublic(new X509EncodedKeySpec(cat(unhex("302a300506032b656e032100"),xPublic)));
      if(seed!=null)sign=sk.generatePrivate(new PKCS8EncodedKeySpec(cat(unhex("302e020100300506032b657004220420"),seed)));
      if(xPrivate!=null)box=xk.generatePrivate(new PKCS8EncodedKeySpec(cat(unhex("302e020100300506032b656e04220420"),xPrivate)));
    }
    public Identity(Identity source){id=source.id.clone();instance=source.instance.clone();boot=source.boot.clone();role=source.role;signPub=source.signPub;boxPub=source.boxPub;sign=source.sign;box=source.box;}
  }
  static byte[] shared(Identity from,Identity to)throws Exception{KeyAgreement k=KeyAgreement.getInstance("X25519",BC);k.init(from.box);k.doPhase(to.boxPub,true);return k.generateSecret();}
  public static final class Context {
    public byte[] domain,from,to,machine,senderInstance,receiverInstance,senderBoot,receiverBoot,connection;
    public long fromRole,toRole,use,generation,seq,issued,deadline,cls,recipientUnknown;
    public Context(Identity a,Identity z,byte[] conn,long use,long gen,long seq,long now,long cls,boolean boot){domain=utf8(boot?"bootstrap":cls==1?"signal":"control");from=a.id;to=z.id;fromRole=a.role;toRole=z.role;machine=a.role==2?a.id:z.id;senderInstance=a.instance;receiverInstance=z.instance;senderBoot=a.boot;receiverBoot=z.boot;connection=conn;this.use=use;generation=gen;this.seq=seq;issued=now;deadline=now+10000;this.cls=cls;}
    Context(){}
    byte[] encode(Wire scope){return pb(b(1,domain),b(2,scope.ACCOUNT),b(3,from),b(4,to),u(5,fromRole),u(6,toRole),b(7,machine),u(8,io.github.lewin671.remoteagent.core.VersionKt.PROTOCOL_VERSION),u(9,scope.epoch),b(10,scope.HEAD),b(11,senderInstance),b(12,receiverInstance),b(13,senderBoot),b(14,receiverBoot),b(15,connection),u(16,use),u(17,generation),u(18,seq),u(19,issued),u(20,deadline),u(21,cls),b(22,from),b(23,scope.ACCOUNT_DOMAIN),u(24,recipientUnknown));}
  }
  public Context context(byte[] raw){Map<Integer,Field> f=Schema.fields(raw,"Context");need(f,"bbbbuubuubbbbbbuuuuuubbu");check(Arrays.equals(f.get(23).b,ACCOUNT_DOMAIN)&&Arrays.equals(f.get(2).b,ACCOUNT)&&f.get(8).u==io.github.lewin671.remoteagent.core.VersionKt.PROTOCOL_VERSION&&f.get(9).u==epoch&&Arrays.equals(f.get(10).b,HEAD)&&Arrays.equals(f.get(22).b,f.get(3).b));Context c=new Context();c.domain=f.get(1).b;c.from=f.get(3).b;c.to=f.get(4).b;c.fromRole=f.get(5).u;c.toRole=f.get(6).u;c.machine=f.get(7).b;c.senderInstance=f.get(11).b;c.receiverInstance=f.get(12).b;c.senderBoot=f.get(13).b;c.receiverBoot=f.get(14).b;c.connection=f.get(15).b;c.use=f.get(16).u;c.generation=f.get(17).u;c.seq=f.get(18).u;c.issued=f.get(19).u;c.deadline=f.get(20).u;c.cls=f.get(21).u;c.recipientUnknown=f.get(24).u;return c;}
  public static int compare(byte[] a,byte[] z){for(int i=0;i<Math.min(a.length,z.length);i++)if(a[i]!=z[i])return (a[i]&255)-(z[i]&255);return a.length-z.length;}
  byte[] wrapInfo(byte[] from,byte[] to){return lp(utf8("remote-agent/peer-context/v1"),ACCOUNT,ACCOUNT_DOMAIN,compare(from,to)<0?from:to,compare(from,to)<0?to:from,from,to);}
  byte[] bodyInfo(Context c){if(Arrays.equals(c.domain,utf8("bootstrap")))return lp(utf8("remote-agent/peer-bootstrap/v1"),ACCOUNT,ACCOUNT_DOMAIN,c.from,c.to);return lp(utf8("remote-agent/peer-body/v1"),ACCOUNT,ACCOUNT_DOMAIN,c.from,c.to,c.senderInstance,c.receiverInstance,c.senderBoot,c.receiverBoot,c.connection,num(c.use),num(c.generation),num(c.cls));}
  byte[] aad(byte[] from,byte[] to,long cls,byte[] id){return lp(utf8("remote-agent/peer-aad/v1"),ACCOUNT,ACCOUNT_DOMAIN,num(io.github.lewin671.remoteagent.core.VersionKt.PROTOCOL_VERSION),pb(b(1,from),b(2,to),u(3,cls),b(4,id)));}
  static byte[] sigInput(byte[] ad,byte[] nc,byte[] ec,byte[] nb,byte[] eb){return lp(utf8("remote-agent/peer-signature/v1"),ad,nc,ec,nb,eb);}
  public byte[] seal(Identity from,Identity to,Context ctx,long type,byte[] payload,byte[] id,byte[] nc,byte[] nb)throws Exception{
    SecureRandom random=new SecureRandom();if(id==null){id=new byte[16];random.nextBytes(id);}if(nc==null){nc=new byte[12];random.nextBytes(nc);}if(nb==null){nb=new byte[12];random.nextBytes(nb);}
    byte[] dh=shared(from,to),ad=aad(from.id,to.id,ctx.cls,id),ec=gcm(kdf(dh,wrapInfo(from.id,to.id)),nc,ad,ctx.encode(this),true);
    byte[] eb=gcm(kdf(dh,bodyInfo(ctx)),nb,lp(ad,hash(ec)),pb(b(1,pb(u(1,type),b(2,payload)))),true);
    Signature signer=Signature.getInstance("Ed25519",BC);signer.initSign(from.sign);signer.update(sigInput(ad,nc,ec,nb,eb));byte[] sig=signer.sign();
    byte[] packet=pb(b(1,from.id),b(2,to.id),u(3,ctx.cls),b(4,id),b(5,pb(b(1,nc),b(2,ec),b(3,nb),b(4,eb),b(5,sig))));check(packet.length<=(ctx.cls==2?16384:65536));return packet;
  }
  public static final class Opened {public Context ctx;public long type;public byte[] payload,frameId,hash,raw;}
  public Opened open(byte[] raw,Identity from,Identity to,long now)throws Exception{return openShared(raw,from,to,now,shared(to,from));}
  public Opened openSent(byte[] raw,Identity from,Identity to,long now)throws Exception{Opened o=openShared(raw,from,to,now,shared(from,to));check(Arrays.equals(o.ctx.senderInstance,from.instance)&&Arrays.equals(o.ctx.senderBoot,from.boot));return o;}
  Opened openShared(byte[] raw,Identity from,Identity to,long now,byte[] dh)throws Exception{
    check(raw.length<=65536);raw=raw.clone();Map<Integer,Field> f=Schema.fields(raw,"PeerFrameDelivery");need(f,"bbubb");long cls=f.get(3).u;byte[] id=f.get(4).b;
    check((cls==1||cls==2)&&raw.length<=(cls==2?16384:65536)&&id.length==16&&Arrays.equals(f.get(1).b,from.id)&&Arrays.equals(f.get(2).b,to.id));
    Map<Integer,Field> p=Schema.fields(f.get(5).b,"SealedPeerPacket");need(p,"bbbbb");byte[] nc=p.get(1).b,ec=p.get(2).b,nb=p.get(3).b,eb=p.get(4).b,sig=p.get(5).b,ad=aad(from.id,to.id,cls,id);
    check(nc.length==12&&nb.length==12&&sig.length==64);Signature verifier=Signature.getInstance("Ed25519",BC);verifier.initVerify(from.signPub);verifier.update(sigInput(ad,nc,ec,nb,eb));check(verifier.verify(sig));
    Context c=context(gcm(kdf(dh,wrapInfo(from.id,to.id)),nc,ad,ec,false));
    check(Arrays.equals(c.from,from.id)&&Arrays.equals(c.to,to.id)&&c.cls==cls&&c.fromRole==from.role&&c.toRole==to.role&&from.role!=to.role&&c.connection.length==16&&c.seq!=0&&Long.compareUnsigned(c.issued,c.deadline)<0&&Long.compareUnsigned(c.deadline-c.issued,30000)<=0&&Long.compareUnsigned(now,c.deadline)<0&&Long.compareUnsigned(c.issued,now+300000)<=0);
    check(Arrays.equals(c.machine,from.role==2?from.id:to.id));boolean boot=Arrays.equals(c.domain,utf8("bootstrap"));
    check(c.senderInstance.length==16&&c.senderBoot.length==32&&c.fromRole>=1&&c.fromRole<=2&&c.toRole>=1&&c.toRole<=2);
    check(c.recipientUnknown==0||c.recipientUnknown==1);if(c.recipientUnknown==1)check(c.receiverInstance.length==0&&c.receiverBoot.length==0);else check(c.receiverInstance.length==16&&c.receiverBoot.length==32&&Arrays.equals(c.receiverInstance,to.instance)&&Arrays.equals(c.receiverBoot,to.boot));
    if(!boot){check(Arrays.equals(c.domain,utf8(cls==1?"signal":"control")));check(Arrays.equals(c.senderInstance,from.instance)&&Arrays.equals(c.receiverInstance,to.instance)&&Arrays.equals(c.senderBoot,from.boot)&&Arrays.equals(c.receiverBoot,to.boot)&&c.generation!=0&&Long.compareUnsigned(c.use,2)<=0);}
    byte[] body=gcm(kdf(dh,bodyInfo(c)),nb,lp(ad,hash(ec)),eb,false);Map<Integer,Field> w=Schema.fields(body,"PeerSignal");need(w,"b");Map<Integer,Field> t=Schema.fields(w.get(1).b,"TypedPayload");need(t,"ub");long type=t.get(1).u;check(boot||cls!=1||c.use!=0||type==8||type==9);check(Long.compareUnsigned(type,0xffffffffL)<=0&&(!boot||(cls==1&&c.use==0&&c.generation==0&&(type==1||type==2))));
    check(c.recipientUnknown!=1||(boot&&cls==1&&type==1));Opened result=new Opened();result.ctx=c;result.type=type;result.payload=t.get(2).b;result.frameId=id;result.hash=hash(raw);result.raw=raw.clone();return result;
  }
  public static void validatePolicyReply(Opened request,Opened reply,long now){
    Context a=request.ctx,z=reply.ctx;check(now<a.deadline&&now<z.deadline&&a.generation==z.generation&&a.use==z.use&&Arrays.equals(a.senderInstance,z.receiverInstance)&&Arrays.equals(a.receiverInstance,z.senderInstance)&&Arrays.equals(a.senderBoot,z.receiverBoot)&&Arrays.equals(a.receiverBoot,z.senderBoot));check(a.cls==2&&z.cls==2&&request.type==9&&reply.type==13&&Arrays.equals(a.from,z.to)&&Arrays.equals(a.to,z.from)&&Arrays.equals(a.connection,z.connection));
    Map<Integer,Field> q=Schema.fields(request.payload,"DirectPolicy"),r=Schema.fields(reply.payload,"PolicyReply");need(q,"buuu");need(r,"uuuubub");
    check(q.get(2).u!=0&&Long.compareUnsigned(q.get(3).u,1)<=0&&Long.compareUnsigned(r.get(2).u,1)<=0&&Long.compareUnsigned(r.get(3).u,1)<=0);
    check((q.get(4).u==1&&q.get(1).b.length==0)||(q.get(4).u==2&&q.get(1).b.length==32));
    check(r.get(1).u==q.get(2).u&&r.get(4).u==a.seq&&r.get(5).b.length==32&&Arrays.equals(r.get(5).b,request.hash)&&r.get(6).u==q.get(4).u&&Arrays.equals(r.get(7).b,q.get(1).b));
    check(r.get(2).u!=1||r.get(3).u==q.get(3).u);
  }
  public static Identity member(Identity peer){Identity p=new Identity(peer);p.instance=new byte[0];p.boot=new byte[0];return p;}
  public static Context probeContext(Identity from,Identity to,byte[] conn,long seq,long now){Context c=new Context(from,member(to),conn,0,0,seq,now,1,true);c.recipientUnknown=1;return c;}
  public static String cacheKey(Context c){return hex(c.from)+"/"+hex(c.senderInstance)+"/"+hex(c.connection)+"/"+Long.toUnsignedString(c.use)+"/"+Long.toUnsignedString(c.generation)+"/"+c.cls+"/"+Long.toUnsignedString(c.seq);}
}
