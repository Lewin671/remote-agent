package example.transportlab;
import java.nio.file.*;
import java.util.*;
public final class SignalVectors {
 public static void main(String[] args)throws Exception{
  Wire.check(System.getenv().getOrDefault("REMOTE_AGENT_HOME","").startsWith("/tmp/"));Signal[] gates=null;Sender.CandidateBatcher batch=null;int cases=0,assertions=0;
  for(String row:Files.readAllLines(Paths.get(args[0]))){String[] p=row.split("\t",-1);switch(p[0]){
   case "CASE":{Wire.Identity a=new Wire.Identity(p[2],Long.parseLong(p[3])),b=new Wire.Identity(p[4],Long.parseLong(p[5]));byte[] conn=Arrays.copyOf(Wire.hash(Wire.utf8("signal/"+p[1])),16);gates=new Signal[]{new Signal(a,b,conn,Long.parseLong(p[6])),new Signal(b,a,conn,Long.parseLong(p[6]))};cases++;break;}
   case "BATCH_CASE":batch=new Sender.CandidateBatcher(Long.parseLong(p[2]));cases++;break;
   case "BATCH_ADD":Wire.check(batch.add(Wire.unhex(p[3]),Long.parseLong(p[2]),Long.parseLong(p[1]))==p[4].equals("1"));assertions++;break;
   case "BATCH_FLUSH":{List<List<byte[]>> groups=batch.flush(Long.parseLong(p[1]),p[2].equals("1"));List<String> sizes=new ArrayList<>();java.io.ByteArrayOutputStream flat=new java.io.ByteArrayOutputStream();for(List<byte[]> g:groups){sizes.add(Integer.toString(g.size()));for(byte[] raw:g)flat.write(raw);}Wire.check(String.join(",",sizes).equals(p[3])&&Wire.hex(flat.toByteArray()).equals(p[4]));assertions++;break;}
   case "POLICY":{Wire.Opened request=Wire.open(Wire.unhex(p[3]),gates[0].self,gates[1].self,1700000000000L),reply=Wire.open(Wire.unhex(p[4]),gates[1].self,gates[0].self,1700000000000L);boolean ok=true;try{Wire.validatePolicyReply(request,reply);}catch(Exception e){ok=false;}if(ok!=p[2].equals("1"))throw new AssertionError(p[1]);assertions++;break;}
   case "CHALLENGE":int which=Integer.parseInt(p[1]);gates[which].rememberChallenge(Wire.open(Wire.unhex(p[2]),gates[which].self,gates[1-which].self,1700000000000L));break;
   case "RX":{boolean ok;try{gates[Integer.parseInt(p[1])].receive(Wire.unhex(p[4]),1700000000000L+Long.parseLong(p[2]));ok=true;}catch(Exception e){ok=false;}if(ok!=p[3].equals("1"))throw new AssertionError("RX case "+cases);assertions++;break;}
   case "LOCAL":{boolean ok;try{gates[Integer.parseInt(p[1])].setLocal(Wire.unhex(p[5]),p[3].equals("1"),Long.parseLong(p[2]));ok=true;}catch(Exception e){ok=false;}if(ok!=p[4].equals("1"))throw new AssertionError("LOCAL "+cases);assertions++;break;}
   case "DTLS":gates[Integer.parseInt(p[1])].dtls=true;break;
   case "NEXT":gates[Integer.parseInt(p[1])].nextGeneration(Long.parseLong(p[2]));break;
   case "TICK":gates[Integer.parseInt(p[1])].tick(1700000000000L+Long.parseLong(p[2]));break;
   case "EXPECT":{Signal s=gates[Integer.parseInt(p[1])];long value;switch(p[2]){case "stale":value=s.staleChallenges;break;case "boot":value=s.bootReady?1:0;break;case "gap_ms":value=s.gapDeadline==0?0:s.gapDeadline-1700000000000L;break;case "sdk":value=s.sdkActions;break;case "duplicates":value=s.duplicates;break;case "conflicts":value=s.conflicts;break;case "unsupported":value=s.unsupported;break;case "pending":value=s.pending.size();break;case "ready":value=s.ready()?1:0;break;case "closed":value=s.closed?1:0;break;case "generation":value=s.generation;break;case "next":value=s.next;break;default:throw new AssertionError(p[2]);}if(value!=Long.parseLong(p[3]))throw new AssertionError(p[2]+" case "+cases+" got "+value);assertions++;break;}
   default:throw new AssertionError(p[0]);
  }}System.out.println("Java signal scenarios: "+cases+", assertions: "+assertions);
 }
}
