# Protobuf lite keeps field names for reflection-based parsing.
-keep class * extends com.google.protobuf.GeneratedMessageLite { *; }
# Bouncy Castle: the peer wire (core/peer/Wire.java) asks the BC JCA provider
# for Ed25519/X25519 by name, and the provider loads its SPI classes by
# reflection, so R8 must keep them or release builds throw
# NoSuchAlgorithmException and direct never authorizes.
-keep class org.bouncycastle.jce.provider.BouncyCastleProvider { *; }
-keep class org.bouncycastle.jcajce.provider.** { *; }
-dontwarn org.bouncycastle.**
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.openjsse.**
-dontwarn javax.naming.**
-dontwarn io.ktor.**
-dontwarn org.slf4j.**
# WebRTC DataChannel-only JNI callbacks; unused audio/video classes can be stripped.
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
