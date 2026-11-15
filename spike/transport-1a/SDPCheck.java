package example.transportlab;

import java.nio.ByteBuffer;
import java.nio.charset.*;
import java.nio.file.*;
import java.util.*;

public final class SDPCheck {
  static void require(boolean ok) {if(!ok)throw new IllegalArgumentException("SDP invalid");}
  static boolean match(String pattern,String value){return value.matches(pattern);}
  public static void validate(byte[] bytes) throws Exception {
    String raw=StandardCharsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT).onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(bytes)).toString();
    require(bytes.length<=60*1024 && raw.endsWith("\r\n") && !raw.contains("\0"));
    require(!raw.matches("(?s).*[\\x0b\\x0c\\x{85}\\x{2028}\\x{2029}].*"));String rest=raw.replace("\r\n",""); require(!rest.contains("\r")&&!rest.contains("\n"));
    List<Map<String,String>> scopes=Arrays.asList(new HashMap<>(),new HashMap<>());int section=0;
    Set<String> allowed=new HashSet<>(Arrays.asList("group msid-semantic fingerprint ice-ufrag ice-pwd ice-options setup mid sctp-port max-message-size end-of-candidates extmap-allow-mixed sendrecv".split(" ")));
    for(String line:raw.substring(0,raw.length()-2).split("\r\n",-1)){
      if(line.startsWith("m=")){section++;require(section==1&&match("m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel",line));}
      require(!line.equals("a=ice-lite"));
      if(line.startsWith("a=")){
        String[] parts=line.substring(2).split(":",2);String key=parts[0],value=parts.length==2?parts[1]:"";
        require(allowed.contains(key)&&!scopes.get(section).containsKey(key));scopes.get(section).put(key,value);
      }else require(match("[vostcm]=.*",line));
    }
    require(section==1);String fp=null;
    for(Map<String,String> scope:scopes)if(scope.containsKey("fingerprint")){
      String v=scope.get("fingerprint");require(match("sha-256 ([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}",v));
      require(fp==null||fp.equalsIgnoreCase(v));fp=v;
    }
    require(fp!=null);Map<String,String> values=new HashMap<>();
    for(String key:Arrays.asList("ice-ufrag","ice-pwd","setup")){
      for(Map<String,String> scope:scopes)if(scope.containsKey(key)){
        String v=scope.get(key);require(!values.containsKey(key)||values.get(key).equals(v));values.put(key,v);
      }
      require(values.containsKey(key));
    }
    require(match("[A-Za-z0-9+/]{4,256}",values.get("ice-ufrag"))&&match("[A-Za-z0-9+/]{22,256}",values.get("ice-pwd")));
    require(Arrays.asList("active","passive","actpass").contains(values.get("setup")));
    String port=scopes.get(1).getOrDefault("sctp-port","");require(match("[0-9]+",port)&&Long.parseLong(port)<=65535);
  }
  public static void main(String[] args) throws Exception {
    require(System.getenv().getOrDefault("REMOTE_AGENT_HOME","").startsWith("/tmp/"));
    Path root=Paths.get(args[0]);int count=0;
    for(String row:Files.readAllLines(root.resolve("vectors.tsv"))){
      String[] p=row.split("\t");boolean valid;
      try{validate(Files.readAllBytes(root.resolve(p[0])));valid=true;}catch(Exception e){valid=false;}
      if(valid!=p[1].equals("1"))throw new AssertionError(p[0]);count++;
    }
    System.out.println("Java SDP vectors: "+count+" passed");
  }
}
