package example.transportlab;
import java.nio.file.*;
import java.util.*;
public final class SignalVectors {
 public static void main(String[] args)throws Exception{
  Wire.check(System.getenv().getOrDefault("REMOTE_AGENT_HOME","").startsWith("/tmp/"));Signal[] gates=null;int cases=0,assertions=0;
  for(String row:Files.readAllLines(Paths.get(args[0]))){String[] p=row.split("\t",-1);switch(p[0]){
   case "CASE":{Wire.Identity a=new Wire.Identity(p[2],Long.parseLong(p[3])),b=new Wire.Identity(p[4],Long.parseLong(p[5]));byte[] conn=Arrays.copyOf(Wire.hash(Wire.utf8("signal/"+p[1])),16);gates=new Signal[]{new Signal(a,b,conn,1),new Signal(b,a,conn,1)};cases++;break;}
   case "CHALLENGE":gates[Integer.parseInt(p[1])].challengeHash=Wire.unhex(p[2]);break;
   case "RX":{boolean ok;try{gates[Integer.parseInt(p[1])].receive(Wire.unhex(p[4]),1700000000000L+Long.parseLong(p[2]));ok=true;}catch(Exception e){ok=false;}if(ok!=p[3].equals("1"))throw new AssertionError("RX case "+cases);assertions++;break;}
   case "LOCAL":{boolean ok;try{gates[Integer.parseInt(p[1])].setLocal(Wire.unhex(p[5]),p[3].equals("1"),Long.parseLong(p[2]));ok=true;}catch(Exception e){ok=false;}if(ok!=p[4].equals("1"))throw new AssertionError("LOCAL "+cases);assertions++;break;}
   case "DTLS":gates[Integer.parseInt(p[1])].dtls=true;break;
   case "NEXT":gates[Integer.parseInt(p[1])].nextGeneration(Long.parseLong(p[2]));break;
   case "TICK":gates[Integer.parseInt(p[1])].tick(1700000000000L+Long.parseLong(p[2]));break;
   case "EXPECT":{Signal s=gates[Integer.parseInt(p[1])];long value;switch(p[2]){case "sdk":value=s.sdkActions;break;case "duplicates":value=s.duplicates;break;case "conflicts":value=s.conflicts;break;case "unsupported":value=s.unsupported;break;case "pending":value=s.pending.size();break;case "ready":value=s.ready()?1:0;break;case "closed":value=s.closed?1:0;break;case "generation":value=s.generation;break;case "next":value=s.next;break;default:throw new AssertionError(p[2]);}if(value!=Long.parseLong(p[3]))throw new AssertionError(p[2]+" case "+cases+" got "+value);assertions++;break;}
   default:throw new AssertionError(p[0]);
  }}System.out.println("Java signal scenarios: "+cases+", assertions: "+assertions);
 }
}
