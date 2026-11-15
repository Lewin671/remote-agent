package example.transportlab;
import android.content.Context;
import java.io.*;
import java.net.*;
import java.nio.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.util.*;
import java.util.concurrent.*;
import org.json.*;
import org.webrtc.*;

final class CrossAndroid {
 static final class Observer implements PeerConnection.Observer,DataChannel.Observer {
  final List<IceCandidate> candidates=Collections.synchronizedList(new ArrayList<>());volatile Semaphore gathered=new Semaphore(0);
  final Semaphore opened=new Semaphore(0),extra=new Semaphore(0),rogue=new Semaphore(0);final Map<String,Semaphore> waiters=new ConcurrentHashMap<>();
  DataChannel channel;int unexpected,received;volatile boolean closed;
  public void onSignalingChange(PeerConnection.SignalingState s){}
  public void onIceConnectionChange(PeerConnection.IceConnectionState s){}
  public void onIceConnectionReceivingChange(boolean b){}
  public synchronized void onIceGatheringChange(PeerConnection.IceGatheringState s){if(s==PeerConnection.IceGatheringState.COMPLETE)gathered.release();}
  public void onIceCandidate(IceCandidate c){candidates.add(c);}
  public void onIceCandidatesRemoved(IceCandidate[] c){}
  public void onAddStream(MediaStream s){} public void onRemoveStream(MediaStream s){}
  PeerConnection pc;
  public void onDataChannel(DataChannel c){unexpected++;closed=true;c.close();new Thread(()->{pc.close();extra.release();},"reject-channel").start();}
  public void onRenegotiationNeeded(){}
  public void onBufferedAmountChange(long amount){}
  public void onStateChange(){if(channel.state()==DataChannel.State.OPEN)opened.release();}
  public void onMessage(DataChannel.Buffer b){byte[] data=new byte[b.data.remaining()];b.data.get(data);String text=new String(data,StandardCharsets.UTF_8);received++;if(text.equals("forbidden-business"))rogue.release();Semaphore s=waiters.get(text);if(s!=null)s.release();}
  synchronized void reset(){candidates.clear();gathered=new Semaphore(0);}
  JSONArray jsonCandidates()throws Exception{JSONArray a=new JSONArray();synchronized(candidates){for(IceCandidate c:candidates){JSONObject j=new JSONObject();j.put("candidate",c.sdp);j.put("sdpMid",c.sdpMid);j.put("sdpMLineIndex",c.sdpMLineIndex);a.put(j);}}return a;}
  void echo(DataChannel c,String text)throws Exception{Semaphore done=new Semaphore(0);waiters.put(text,done);if(!c.send(new DataChannel.Buffer(ByteBuffer.wrap(text.getBytes(StandardCharsets.UTF_8)),false)))throw new Exception("send");AndroidLab.waitFor(done);waiters.remove(text);}
  void probe(DataChannel c,int generation)throws Exception{String text="probe-gen:"+generation+":"+UUID.randomUUID();Semaphore done=new Semaphore(0);waiters.put(text,done);ScheduledExecutorService timer=Executors.newSingleThreadScheduledExecutor();ScheduledFuture<?> task=timer.scheduleAtFixedRate(()->{if(c.state()==DataChannel.State.OPEN)c.send(new DataChannel.Buffer(ByteBuffer.wrap(text.getBytes(StandardCharsets.UTF_8)),false));},0,50,TimeUnit.MILLISECONDS);boolean ok;try{ok=done.tryAcquire(2500,TimeUnit.MILLISECONDS);}finally{task.cancel(false);timer.shutdown();timer.awaitTermination(1,TimeUnit.SECONDS);waiters.remove(text);}if(!ok)throw new Exception("probe deadline");}
 }
 static final class Peer {
  final String url,token,id;String last="initial";
  Peer(JSONObject config,String id)throws Exception{url=config.getString("url").replace("127.0.0.1","10.0.2.2");token=config.getString("token");this.id=id;}
  JSONObject call(String op,JSONObject fields,boolean reject)throws Exception{
   last=op;fields.put("id",id);fields.put("operation",op);fields.put("android",true);
   HttpURLConnection c=(HttpURLConnection)new URL(url).openConnection();c.setRequestMethod("POST");c.setConnectTimeout(3000);c.setReadTimeout(10000);c.setDoOutput(true);c.setRequestProperty("Authorization","Bearer "+token);c.setRequestProperty("Content-Type","application/json");
   try{byte[] body=fields.toString().getBytes(StandardCharsets.UTF_8);c.setFixedLengthStreamingMode(body.length);try(OutputStream o=c.getOutputStream()){o.write(body);}int status=c.getResponseCode();if(reject){if(status!=400)throw new Exception("expected rejection");return new JSONObject();}if(status!=200)throw new Exception("peer "+op);ByteArrayOutputStream data=new ByteArrayOutputStream();try(InputStream in=c.getInputStream()){byte[] b=new byte[4096];int n;while((n=in.read(b))!=-1)data.write(b,0,n);}String text=data.toString("UTF-8");return text.trim().equals("null")?new JSONObject():new JSONObject(text);}finally{c.disconnect();}
  }
  JSONObject call(String op)throws Exception{return call(op,new JSONObject(),false);}
 }
 static JSONObject obj(Object...pairs)throws Exception{JSONObject v=new JSONObject();for(int i=0;i<pairs.length;i+=2)v.put((String)pairs[i],pairs[i+1]);return v;}
 static String ufrag(String s)throws Exception{for(String line:s.split("\r\n"))if(line.startsWith("a=ice-ufrag:"))return line.substring(12);throw new Exception("ufrag");}
 static String b64(String s){return Base64.getEncoder().encodeToString(s.getBytes(StandardCharsets.UTF_8));}
 static SessionDescription remote(JSONObject reply,SessionDescription.Type type)throws Exception{byte[] raw=Base64.getDecoder().decode(reply.getString("sdp"));SDPCheck.validate(raw);String text=new String(raw,StandardCharsets.UTF_8);if(!Arrays.equals(raw,text.getBytes(StandardCharsets.UTF_8)))throw new Exception("raw SDP");return new SessionDescription(type,text);}
 static SessionDescription local(PeerConnection pc,Observer o,boolean answer)throws Exception{SessionDescription s=AndroidLab.create(pc,answer,false);SDPCheck.validate(s.description.getBytes(StandardCharsets.UTF_8));o.reset();AndroidLab.install(pc,s,true);AndroidLab.waitFor(o.gathered);return s;}
 static void add(PeerConnection pc,JSONArray cs)throws Exception{for(int i=0;i<cs.length();i++){JSONObject c=cs.getJSONObject(i);if(!pc.addIceCandidate(new IceCandidate(c.optString("sdpMid","0"),c.optInt("sdpMLineIndex",0),c.getString("candidate"))))throw new Exception("add");}}
 static void run(Context context,JSONObject config,File directory)throws Exception{
  PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(context).setEnableInternalTracer(false).setInjectableLogger((m,s,t)->{},Logging.Severity.LS_NONE).createInitializationOptions());
  PeerConnectionFactory factory=PeerConnectionFactory.builder().createPeerConnectionFactory();JSONArray results=new JSONArray();
  try{for(String mode:new String[]{"client-offer","peer-offer","dcep","undeclared"}){
   long start=System.nanoTime();Peer remote=new Peer(config,"android-"+mode);Observer observer=new Observer(),probe=new Observer();
   PeerConnection.RTCConfiguration cfg=new PeerConnection.RTCConfiguration(Collections.emptyList());cfg.sdpSemantics=PeerConnection.SdpSemantics.UNIFIED_PLAN;
   PeerConnection pc=factory.createPeerConnection(cfg,observer);observer.pc=pc;probe.pc=pc;
   List<DataChannel> channels=new ArrayList<>();for(int id=0;id<2;id++){DataChannel.Init init=new DataChannel.Init();init.negotiated=true;init.id=id;init.ordered=id==0;if(id==1)init.maxRetransmits=0;DataChannel dc=pc.createDataChannel("lab-"+id,init);Observer o=id==0?observer:probe;o.channel=dc;dc.registerObserver(o);channels.add(dc);}
   try{
    int gen=1;JSONObject reply;SessionDescription ours;
    if(mode.equals("client-offer")){ours=local(pc,observer,false);reply=remote.call("answer-offer",obj("sdp",b64(ours.description)),false);AndroidLab.install(pc,remote(reply,SessionDescription.Type.ANSWER),false);}
    else{reply=remote.call("offer");AndroidLab.install(pc,remote(reply,SessionDescription.Type.OFFER),false);ours=local(pc,observer,true);remote.call("install-answer",obj("sdp",b64(ours.description),"generation",gen),false);}
    String old=ufrag(ours.description);JSONObject before=remote.call("stats");remote.call("candidates",obj("generation",2,"ufrag",old,"candidates",observer.jsonCandidates(),"skip_remote_candidates",true),true);remote.call("candidates",obj("generation",1,"ufrag","wrong","candidates",observer.jsonCandidates(),"skip_remote_candidates",true),true);JSONObject after=remote.call("stats");if(before.getInt("add_calls")!=after.getInt("add_calls"))throw new Exception("negative SDK call");
    remote.call("candidates",obj("generation",gen,"ufrag",old,"candidates",observer.jsonCandidates(),"skip_remote_candidates",true),false);add(pc,reply.getJSONArray("candidates"));AndroidLab.waitFor(observer.opened);AndroidLab.waitFor(probe.opened);double initial=(System.nanoTime()-start)/1e6;
    observer.echo(channels.get(0),"android-cross-"+mode);probe.echo(channels.get(1),"probe:android-"+mode);
    JSONObject stats0=remote.call("stats");channels.get(1).send(new DataChannel.Buffer(ByteBuffer.wrap("business-on-probe".getBytes(StandardCharsets.UTF_8)),false));observer.echo(channels.get(0),"barrier-"+mode);observer.rogue.tryAcquire(100,TimeUnit.MILLISECONDS);JSONObject stats1=remote.call("stats");if(stats1.getInt("probe_business_rejected")!=stats0.getInt("probe_business_rejected")+1)throw new Exception("probe business");
    Double restart=null;
    if(mode.equals("peer-offer")){long t=System.nanoTime();reply=remote.call("restart-offer");gen=reply.getInt("generation");AndroidLab.install(pc,remote(reply,SessionDescription.Type.OFFER),false);ours=local(pc,observer,true);String current=ufrag(ours.description);if(current.equals(old))throw new Exception("restart credentials");remote.call("install-answer",obj("sdp",b64(ours.description),"generation",gen),false);remote.call("candidates",obj("generation",gen,"ufrag",current,"candidates",observer.jsonCandidates(),"skip_remote_candidates",true),false);add(pc,reply.getJSONArray("candidates"));probe.probe(channels.get(1),gen);observer.echo(channels.get(0),"restart-android");restart=(System.nanoTime()-t)/1e6;}
    if(mode.equals("dcep")){remote.call("rogue-dcep");AndroidLab.waitFor(observer.extra);if(observer.unexpected!=1||!observer.closed)throw new Exception("DCEP");}
    if(mode.equals("undeclared")){remote.call("rogue-negotiated");observer.echo(channels.get(0),"undeclared-barrier");if(observer.rogue.tryAcquire(150,TimeUnit.MILLISECONDS)||observer.unexpected!=0)throw new Exception("undeclared");}
    JSONObject row=obj("mode",mode,"elapsed_ms",(System.nanoTime()-start)/1e6,"initial_ready_ms",initial,"extra_dcep_rejected",observer.unexpected,"candidate_negative_before_sdk",true,"probe_business_rejected",true,"original_passed_to_sdk",true);if(restart!=null)row.put("restart_ready_ms",restart);results.put(row);Files.write(new File(directory,"cross-android.json").toPath(),results.toString(2).getBytes(StandardCharsets.UTF_8));
   }catch(Exception e){Files.write(new File(directory,"cross-stage.json").toPath(),obj("mode",mode,"last_operation",remote.last,"error_class",e.getClass().getName()).toString().getBytes(StandardCharsets.UTF_8));throw e;}
   finally{for(DataChannel dc:channels){dc.unregisterObserver();dc.close();}pc.close();for(DataChannel dc:channels)dc.dispose();pc.dispose();try{remote.call("close");}catch(Exception ignored){}}
  }}finally{factory.dispose();}
 }
}
