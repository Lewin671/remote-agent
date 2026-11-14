package io.github.lewin671.remoteagent.core

import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okio.Buffer
import okio.BufferedSink

/** A fixture inspects non-command bytes before sending exactly those same bytes.
 * Reading a one-shot body consumes it; the inspected bytes form the next hop's
 * one-shot body. Never use this to bypass Forward's transport replay tests. */
internal fun Request.inspectBody(inspect: (ByteArray) -> Unit): Request {
    check(!url.encodedPath.endsWith("/Forward"))
    val original = checkNotNull(body)
    val buffer = Buffer()
    original.writeTo(buffer)
    val bytes = buffer.readByteArray()
    inspect(bytes.copyOf())
    val next = bytes.toRequestBody(original.contentType())
    return newBuilder().method(method, object : RequestBody() {
        override fun contentType() = next.contentType()
        override fun contentLength() = next.contentLength()
        override fun isOneShot() = true
        override fun writeTo(sink: BufferedSink) = next.writeTo(sink)
    }).build()
}
