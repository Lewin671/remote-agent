package example.transportlab;

import android.app.Activity;
import android.os.Bundle;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.io.File;
import java.util.*;
import java.util.concurrent.*;
import org.json.JSONObject;
import org.webrtc.*;

// Standalone SDK test app. No product, account, audio/video tracks, or STUN.
public final class AndroidLab extends Activity {
  volatile String stage="initial";
  Observer debugA,debugB;
  static final class Observer implements PeerConnection.Observer, DataChannel.Observer {
    final List<IceCandidate> candidates = Collections.synchronizedList(new ArrayList<>());
    final Semaphore gathered=new Semaphore(0), opened=new Semaphore(0), message=new Semaphore(0);
    DataChannel channel; int unexpected; volatile String ice="new",dcState="new";
    public void onSignalingChange(PeerConnection.SignalingState s) {}
    public void onIceConnectionChange(PeerConnection.IceConnectionState s) {ice=s.name();}
    public void onIceConnectionReceivingChange(boolean b) {}
    public void onIceGatheringChange(PeerConnection.IceGatheringState s) { if(s==PeerConnection.IceGatheringState.COMPLETE)gathered.release(); }
    public void onIceCandidate(IceCandidate c) {candidates.add(c);}
    public void onIceCandidatesRemoved(IceCandidate[] c) {}
    public void onAddStream(MediaStream s) {}
    public void onRemoveStream(MediaStream s) {}
    public void onDataChannel(DataChannel dc) {unexpected++;dc.close();}
    public void onRenegotiationNeeded() {}
    public void onBufferedAmountChange(long previous) {}
    public void onStateChange() {dcState=channel.state().name();if(channel.state()==DataChannel.State.OPEN)opened.release();}
    public void onMessage(DataChannel.Buffer b) {
      byte[] raw=new byte[b.data.remaining()]; b.data.get(raw);
      if(new String(raw,StandardCharsets.UTF_8).equals("lab-message"))message.release();
    }
  }
  static final class SDP implements SdpObserver {
    final CompletableFuture<SessionDescription> result=new CompletableFuture<>();
    public void onCreateSuccess(SessionDescription s){result.complete(s);}
    public void onSetSuccess(){result.complete(null);}
    public void onCreateFailure(String s){result.completeExceptionally(new Exception("create failed"));}
    public void onSetFailure(String s){result.completeExceptionally(new Exception("install failed"));}
    SessionDescription get() throws Exception{return result.get(10,TimeUnit.SECONDS);}
  }
  static void waitFor(Semaphore s) throws Exception {if(!s.tryAcquire(10,TimeUnit.SECONDS))throw new Exception("deadline");}
  static SessionDescription create(PeerConnection pc, boolean answer, boolean restart) throws Exception {
    SDP cb=new SDP(); MediaConstraints c=new MediaConstraints();
    if(restart)c.mandatory.add(new MediaConstraints.KeyValuePair("IceRestart","true"));
    if(answer)pc.createAnswer(cb,c);else pc.createOffer(cb,c);return cb.get();
  }
  static void install(PeerConnection pc,SessionDescription s,boolean local) throws Exception {
    SDPCheck.validate(s.description.getBytes(StandardCharsets.UTF_8));
    if(!s.description.endsWith("\r\n")||s.description.contains("\0")||s.description.contains("m=audio")||s.description.contains("m=video")||s.description.contains("a=ice-lite"))throw new Exception("SDP invalid");
    SDP cb=new SDP();if(local)pc.setLocalDescription(cb,s);else pc.setRemoteDescription(cb,s);cb.get();
  }
  void save(String name,String text)throws Exception{Files.write(new File(getFilesDir(),name).toPath(),text.getBytes(StandardCharsets.UTF_8));}
  void experiment() throws Exception {
    long start=System.nanoTime();
    PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(this)
      .setEnableInternalTracer(false).setInjectableLogger((m,s,t)->{},Logging.Severity.LS_NONE).createInitializationOptions());
    PeerConnectionFactory factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
    Observer a=new Observer(), b=new Observer();debugA=a;debugB=b;
    PeerConnection.RTCConfiguration cfg=new PeerConnection.RTCConfiguration(Collections.emptyList());
    cfg.sdpSemantics=PeerConnection.SdpSemantics.UNIFIED_PLAN;
    PeerConnection pa=factory.createPeerConnection(cfg,a),pb=factory.createPeerConnection(cfg,b);
    List<DataChannel> channels=new ArrayList<>();
    try {
      for(int side=0;side<2;side++)for(int id=0;id<2;id++){
        DataChannel.Init init=new DataChannel.Init();init.negotiated=true;init.id=id;init.ordered=id==0;if(id==1)init.maxRetransmits=0;
        DataChannel dc=(side==0?pa:pb).createDataChannel("lab-"+id,init);channels.add(dc);
        if(id==0){Observer o=side==0?a:b;o.channel=dc;dc.registerObserver(o);}
      }
      SessionDescription offer=create(pa,false,false);save("android-offer.sdp",offer.description);
      stage="gather_a";install(pa,offer,true);waitFor(a.gathered);install(pb,offer,false);
      SessionDescription answer=create(pb,true,false);save("android-answer.sdp",answer.description);
      stage="gather_b";install(pb,answer,true);waitFor(b.gathered);install(pa,answer,false);
      boolean exact=pa.getRemoteDescription().description.equals(answer.description)&&pb.getRemoteDescription().description.equals(offer.description);
      for(IceCandidate c:a.candidates)if(!pb.addIceCandidate(c))throw new Exception("candidate rejected");
      for(IceCandidate c:b.candidates)if(!pa.addIceCandidate(c))throw new Exception("candidate rejected");
      stage="open_a";waitFor(a.opened);stage="open_b";waitFor(b.opened);
      if(!a.channel.send(new DataChannel.Buffer(ByteBuffer.wrap("lab-message".getBytes(StandardCharsets.UTF_8)),true)))throw new Exception("send failed");
      stage="message";waitFor(b.message);SessionDescription restart=create(pa,false,true);save("android-restart.sdp",restart.description);
      JSONObject result=new JSONObject();result.put("sdk","io.github.webrtc-sdk:android:150.7871.04");
      result.put("elapsed_ms",(System.nanoTime()-start)/1e6);result.put("message",true);
      result.put("candidates",new org.json.JSONArray(Arrays.asList(a.candidates.size(),b.candidates.size())));
      result.put("negotiated_ids",new org.json.JSONArray(Arrays.asList(0,1)));result.put("dcep_callbacks",a.unexpected+b.unexpected);
      result.put("remote_description_matches_before_candidates",exact);save("android.json",result.toString(2));
    } finally {
      for(DataChannel c:channels){c.unregisterObserver();c.close();}
      pa.close();pb.close();for(DataChannel c:channels)c.dispose();pa.dispose();pb.dispose();factory.dispose();
    }
  }
  @Override public void onCreate(Bundle state) {
    super.onCreate(state);
    try{save("pid",Integer.toString(android.os.Process.myPid()));}catch(Exception ignored){}
    new Thread(()->{try{File config=new File(getFilesDir(),"peer.json");if(config.exists()){CrossAndroid.run(this,new JSONObject(new String(Files.readAllBytes(config.toPath()),StandardCharsets.UTF_8)),getFilesDir());}else{experiment();}}catch(Exception e){try{save("failure.txt",e.getClass().getName()+" stage="+stage);}catch(Exception ignored){}}
      finally{try{save("done","done");}catch(Exception ignored){}}},"sdk-lab").start();
  }
}
