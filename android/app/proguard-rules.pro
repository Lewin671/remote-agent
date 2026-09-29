# Protobuf lite keeps field names for reflection-based parsing.
-keep class * extends com.google.protobuf.GeneratedMessageLite { *; }
# Bouncy Castle lightweight classes are referenced directly; nothing to keep
# by name. Silence warnings about optional JCA and LDAP parts.
-dontwarn org.bouncycastle.**
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.openjsse.**
-dontwarn javax.naming.**
-dontwarn io.ktor.**
-dontwarn org.slf4j.**
