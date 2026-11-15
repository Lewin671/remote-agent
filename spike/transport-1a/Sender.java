package example.transportlab;
import java.util.*;
final class Sender {
 static final class Pending {byte[] raw,hash;long first,notBefore,expiry,binding;int attempts;}
 final long deadline;final Map<String,Pending> pending=new LinkedHashMap<>();boolean timedOut;int frames,bytes,failures;long retryAt,networkGeneration,binding=1,clock;
 Sender(long deadline){this.deadline=deadline;}
 synchronized boolean enqueue(byte[] raw,long now,long expiry,long binding){clock=Math.max(clock,now);now=clock;if(timedOut||binding!=this.binding||now>=expiry||now>=deadline||pending.size()>=32)return false;byte[] hash=Wire.hash(raw);String key=Wire.hex(hash);if(pending.containsKey(key))return true;Pending p=new Pending();p.raw=raw.clone();p.hash=hash;p.first=now;p.expiry=Math.min(expiry,deadline);p.binding=binding;pending.put(key,p);return true;}
 synchronized List<byte[]> tick(long now){clock=Math.max(clock,now);now=clock;if(now>=deadline){pending.clear();timedOut=true;return Collections.emptyList();}List<byte[]> out=new ArrayList<>();for(String key:new ArrayList<>(pending.keySet())){Pending p=pending.get(key);if(now>=p.expiry||p.binding!=binding){pending.remove(key);continue;}if(p.attempts==3)continue;long due=Math.max(p.first+new long[]{0,500,1500}[p.attempts],p.notBefore);if(now>=due){Wire.check(Arrays.equals(Wire.hash(p.raw),p.hash));out.add(p.raw.clone());p.attempts++;frames++;bytes+=p.raw.length;}}return out;}
 synchronized boolean admit(byte[] raw,long now){clock=Math.max(clock,now);Pending p=pending.get(Wire.hex(Wire.hash(raw)));return p!=null&&!timedOut&&p.binding==binding&&clock<p.expiry&&clock<deadline;}
 synchronized boolean ack(byte[] hash){return pending.remove(Wire.hex(hash))!=null;}
 synchronized void limited(byte[] hash,long now,long delay){Pending p=pending.get(Wire.hex(hash));if(p!=null)p.notBefore=Math.max(p.notBefore,delay>deadline-now?deadline:now+delay);}
 long backoff(long now,double fraction){long cap=failures<5?1000L<<failures:30000;failures++;retryAt=now+(long)(Math.min(30000,cap)*fraction);return retryAt;}
 synchronized void rebind(long value){binding=value;pending.clear();}
 void networkChanged(long g){networkGeneration=Math.max(networkGeneration,g);}

 static final class CandidateBatcher {
  final long generation,deadline,binding;long due;int count,batches;boolean ended;final List<byte[]> pending=new ArrayList<>();CandidateBatcher(long g,long deadline,long binding){generation=g;this.deadline=deadline;this.binding=binding;}
  boolean add(byte[] raw,long g,long now,long binding){if(now>=deadline){ended=true;pending.clear();due=0;}if(binding!=this.binding||ended||g!=generation||count>=64||raw.length>1024)return false;if(pending.isEmpty())due=now+50;pending.add(raw.clone());count++;return true;}
  List<List<byte[]>> flush(long now,boolean end,long g,long binding){if(now>=deadline||g!=generation||binding!=this.binding){ended=true;pending.clear();due=0;return Collections.emptyList();}if(end)ended=true;if(!end&&now<due)return Collections.emptyList();List<List<byte[]>> out=new ArrayList<>();while(pending.size()>=16||(end&&!pending.isEmpty())){int n=Math.min(16,pending.size());out.add(new ArrayList<>(pending.subList(0,n)));pending.subList(0,n).clear();Wire.check(++batches<=4);}due=pending.isEmpty()?0:now+50;return out;}
 }
}
