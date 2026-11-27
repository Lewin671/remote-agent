# Protobuf lite keeps field names for reflection-based parsing.
-keep class * extends com.google.protobuf.GeneratedMessageLite { *; }
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.openjsse.**
-dontwarn javax.naming.**
-dontwarn org.slf4j.**
# WebRTC: libjingle_peerconnection's JNI_OnLoad and callbacks look classes up by
# name (org.jni_zero.JniZero, WebRtcClassLoader and others), so R8 must not strip or rename any of
# org.webrtc; a missing one aborts the process when direct first loads WebRTC.
-keep class org.webrtc.** { *; }
-keep class org.jni_zero.** { *; }
-dontwarn org.jni_zero.**
-keep @interface org.webrtc.CalledByNative
-keep @interface org.webrtc.CalledByNativeUnchecked
-keepclasseswithmembers class * {
    @org.webrtc.CalledByNative <methods>;
    @org.webrtc.CalledByNative <init>(...);
}
-keepclasseswithmembers class * {
    @org.webrtc.CalledByNativeUnchecked <methods>;
}
-keep class org.webrtc.PeerConnectionFactory { *; }
-keep class org.webrtc.PeerConnection$Observer { *; }
-keep class org.webrtc.DataChannel$Observer { *; }
-keep class org.webrtc.SdpObserver { *; }
-keep class org.webrtc.AddIceObserver { *; }
-dontwarn org.webrtc.**
# Gomobile / gobind JNI runtime and corebind bindings.
-keep class go.** { *; }
-keep class io.github.lewin671.remoteagent.corebind.** { *; }
