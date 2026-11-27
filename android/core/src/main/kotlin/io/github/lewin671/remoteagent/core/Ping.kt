// 连接检测: server /healthz round-trip measurement state.
package io.github.lewin671.remoteagent.core

/** Server probe state: being measured, its median round-trip time, or why it failed. */
sealed interface Ping {
    data object Wait : Ping
    data class Ok(val ms: Long) : Ping
    data class Fail(val error: String) : Ping
}

/** A server /healthz round trip at or above this threshold is marked slow. */
const val SLOW_SERVER_MS = 500L

/** The middle one of the samples: one slow packet does not make the line slow. */
fun median(samples: List<Long>): Long = samples.sorted()[samples.size / 2]
