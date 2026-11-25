package io.github.lewin671.remoteagent.core.transport

class PathChanged : Exception()

internal interface FrameStream {
    fun send(frame: ByteArray): Boolean
    fun cancel()
    fun close(): Boolean
}

internal open class FrameListener {
    open fun onOpen(stream: FrameStream) {}
    open fun onFrame(stream: FrameStream, bytes: ByteArray) {}
    open fun onClosing(stream: FrameStream, code: Int) {}
    open fun onEnd(stream: FrameStream, code: Int) {}
    open fun onFailure(stream: FrameStream, error: Throwable) {}
}

/** Physical stream transport; unary RPCs and routing live in Core. */
internal interface Path {
    fun openStream(address: String, listener: FrameListener): FrameStream
}
