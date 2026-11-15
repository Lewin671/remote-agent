package io.github.lewin671.remoteagent.core.peer;
import java.util.*;
import java.io.*;

final class Schema {
 static final class Rule{final int n;final String kind;final boolean repeated;Rule(int n,String k,boolean r){this.n=n;kind=k;repeated=r;}}
 static final String[] SIGNAL={"","Probe","Challenge","SDP","SDP","Candidates","EndCandidates","Confirm","Restart","Close"};
 static final String[] CONTROL={"","Capabilities","AttachPath","InputStatus","Ack","Replay","Barrier","StreamFailed","ClosePath","DirectPolicy","Unsupported","InputStatusReply","BarrierReply","PolicyReply","CapabilitySnapshot"};
 static byte[] known(long cls,long type,byte[] raw){String[] names=cls==2?CONTROL:SIGNAL;return type>0&&type<names.length?normalize(raw,names[(int)type],0,true):raw;}
 static int cap(String name,int n){return name.equals("Candidates")&&n==1?16:name.equals("Restart")&&n==1?2:name.equals("Capabilities")?64:128;}
 static long max(String kind){if(kind.equals("bool"))return 1;if(kind.equals("u32"))return 0xffffffffL;if(kind.equals("i32"))return 0x7fffffffL;if(kind.equals("i64"))return Long.MAX_VALUE;if(kind.startsWith("e"))return Long.parseLong(kind.substring(1));return -1L;}
 static byte[] normalize(byte[] raw,String name){return normalize(raw,name,0,false);}
 static byte[] normalize(byte[] raw,String name,int depth,boolean shape){
  Rule[] defs=SchemaData.rules.get(name);Wire.check(defs!=null&&depth<=8&&raw.length<=(name.equals("Candidate")?1024:name.equals("LogicalFrame")?1<<20:65536));
  Map<Integer,Rule> spec=new HashMap<>();Map<Integer,List<Wire.Field>> values=new HashMap<>();for(Rule r:defs)spec.put(r.n,r);Wire.Reader reader=new Wire.Reader(raw);int last=0;
  while(reader.at<raw.length){long tag=reader.uint();Wire.check((tag>>>3)<=4095);int n=(int)(tag>>>3),wire=(int)(tag&7);Rule r=spec.get(n);Wire.check(r!=null&&n>=last&&(r.repeated||!values.containsKey(n)));last=n;List<Wire.Field> list=values.getOrDefault(n,new ArrayList<>());boolean numeric=!r.kind.equals("b")&&!r.kind.startsWith("m");
   if(numeric&&wire==0){long value=reader.uint();Wire.check(Long.compareUnsigned(value,max(r.kind))<=0);list.add(Wire.u(n,value));}
   else if(wire==2){long size=reader.uint();Wire.check(Long.compareUnsigned(size,raw.length-reader.at)<=0);byte[] body=Arrays.copyOfRange(raw,reader.at,reader.at+(int)size);reader.at+=(int)size;
    if(numeric){Wire.check(r.repeated);Wire.Reader packed=new Wire.Reader(body);while(packed.at<body.length){long value=packed.uint();Wire.check(Long.compareUnsigned(value,max(r.kind))<=0);list.add(Wire.u(n,value));}}
    else{if(r.kind.startsWith("m"))normalize(body,r.kind.substring(1),depth+1,shape);list.add(Wire.b(n,body));}
   }else throw new IllegalArgumentException("schema wire");Wire.check(list.size()<=cap(name,n));values.put(n,list);
  }
  Wire.check(!shape||SchemaRelations.valid(name,values));ByteArrayOutputStream out=new ByteArrayOutputStream();for(Rule r:defs){List<Wire.Field> list=values.getOrDefault(r.n,new ArrayList<>());if(list.isEmpty()&&!r.repeated&&!r.kind.startsWith("m"))list.add(r.kind.equals("b")?Wire.b(r.n,new byte[0]):Wire.u(r.n,0));for(Wire.Field f:list){if(shape&&SchemaBounds.bounds.containsKey(name)&&SchemaBounds.bounds.get(name).containsKey(r.n)){long[] b=SchemaBounds.bounds.get(name).get(r.n);long v=f.bytes?f.b.length:f.u;Wire.check(Long.compareUnsigned(v,b[0])>=0&&Long.compareUnsigned(v,b[1])<=0);}byte[] encoded=Wire.pb(f);out.write(encoded,0,encoded.length);}}return out.toByteArray();
 }
 static Map<Integer,Wire.Field> fields(byte[] raw,String name){raw=normalize(raw,name);Map<Integer,Wire.Field> out=new HashMap<>();Wire.Reader r=new Wire.Reader(raw);while(r.at<raw.length){long tag=r.uint();int n=(int)(tag>>>3);Wire.Field f;if((tag&7)==0)f=Wire.u(n,r.uint());else{int size=(int)r.uint();f=Wire.b(n,Arrays.copyOfRange(raw,r.at,r.at+size));r.at+=size;}out.putIfAbsent(n,f);}return out;}
 static Map<Long,Long> restart(byte[] raw){raw=normalize(raw,"Restart");Wire.Reader r=new Wire.Reader(raw);Map<Long,Long> targets=new HashMap<>();while(r.at<raw.length&&raw[r.at]==10){r.at++;long size=r.uint();Wire.check(size>=0&&size<=raw.length-r.at);Map<Integer,Wire.Field> f=fields(Arrays.copyOfRange(raw,r.at,r.at+(int)size),"RestartPC");r.at+=(int)size;Wire.check(f.get(1).u>=1&&f.get(1).u<=2&&!targets.containsKey(f.get(1).u));targets.put(f.get(1).u,f.get(2).u);}Map<Integer,Wire.Field> tail=Wire.parse(Arrays.copyOfRange(raw,r.at,raw.length),1);Wire.check(!targets.isEmpty()&&tail.size()==1&&tail.containsKey(2)&&!tail.get(2).bytes);return targets;}
}
