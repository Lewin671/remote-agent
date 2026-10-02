// Binary protobuf for connect-kotlin on the javalite runtime.
package io.github.lewin671.remoteagent.core

import com.connectrpc.AnyError
import com.connectrpc.CODEC_NAME_PROTO
import com.connectrpc.Codec
import com.connectrpc.ConnectErrorDetail
import com.connectrpc.ErrorDetailParser
import com.connectrpc.SerializationStrategy
import com.google.protobuf.CodedInputStream
import com.google.protobuf.CodedOutputStream
import com.google.protobuf.ExtensionRegistryLite
import com.google.protobuf.Internal
import com.google.protobuf.MessageLite
import okio.Buffer
import okio.BufferedSource
import okio.ByteString.Companion.toByteString
import kotlin.reflect.KClass

/**
 * connect-kotlin's GoogleJavaLiteProtobufStrategy (Apache License 2.0),
 * with Class.isAssignableFrom where it calls KClass.isSubclassOf. That call
 * was the app's only use of kotlin-reflect: a megabyte in the APK, loaded on
 * the first request, and a sixth of the code R8 optimizes on every release.
 */
internal object LiteProtobufStrategy : SerializationStrategy {
    override fun serializationName(): String = CODEC_NAME_PROTO

    override fun <E : Any> codec(clazz: KClass<E>): Codec<E> = LiteCodec(messageClass(clazz), clazz.java)

    override fun errorDetailParser(): ErrorDetailParser = LiteErrorParser
}

private fun messageClass(clazz: KClass<*>): Class<out MessageLite> {
    if (!MessageLite::class.java.isAssignableFrom(clazz.java)) {
        throw RuntimeException("class ${clazz.java.name} does not extend MessageLite")
    }
    return clazz.java.asSubclass(MessageLite::class.java)
}

private class LiteCodec<E : Any>(message: Class<out MessageLite>, private val type: Class<E>) : Codec<E> {
    private val instance by lazy { Internal.getDefaultInstance(message) }

    override fun encodingName(): String = CODEC_NAME_PROTO

    override fun deserialize(source: BufferedSource): E =
        type.cast(instance.parserForType.parseFrom(source.inputStream(), ExtensionRegistryLite.getEmptyRegistry()))

    override fun serialize(message: E): Buffer = serialize(message, false)

    override fun deterministicSerialize(message: E): Buffer = serialize(message, true)

    private fun serialize(message: E, deterministic: Boolean): Buffer {
        message as MessageLite
        val result = ByteArray(message.serializedSize)
        val output = CodedOutputStream.newInstance(result)
        if (deterministic) output.useDeterministicSerialization()
        message.writeTo(output)
        output.checkNoSpaceLeft()
        return Buffer().write(result)
    }
}

/** The tag of a string, bytes or message field: wire type 2. */
private fun lengthDelimited(field: Int): Int = (field shl 3) or 2

private object LiteErrorParser : ErrorDetailParser {
    override fun <E : Any> unpack(any: AnyError, clazz: KClass<E>): E? =
        clazz.java.cast(Internal.getDefaultInstance(messageClass(clazz)).parserForType.parseFrom(any.value.toByteArray()))

    /**
     * The details of a google.rpc.Status, which only the gRPC protocols
     * send: field 3 repeats google.protobuf.Any, a type URL and a value.
     */
    override fun parseDetails(bytes: ByteArray): List<ConnectErrorDetail> {
        val details = mutableListOf<ConnectErrorDetail>()
        val status = CodedInputStream.newInstance(bytes)
        while (true) {
            val tag = status.readTag()
            if (tag == 0) break
            if (tag != lengthDelimited(3)) {
                status.skipField(tag)
                continue
            }
            val any = status.readBytes().newCodedInput()
            var type = ""
            var value = com.google.protobuf.ByteString.EMPTY
            while (true) {
                when (val field = any.readTag()) {
                    0 -> break
                    lengthDelimited(1) -> type = any.readStringRequireUtf8()
                    lengthDelimited(2) -> value = any.readBytes()
                    else -> any.skipField(field)
                }
            }
            details += ConnectErrorDetail(type = type, payload = value.toByteArray().toByteString())
        }
        return details
    }
}
