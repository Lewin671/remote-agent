// 连接检测: what one stretch of the way to a computer measured.
package io.github.lewin671.remoteagent.core

/** One stretch's round trip: being measured, its time, why it failed, or left out because a nearer stretch failed. */
sealed interface Ping {
    data object Wait : Ping
    data class Ok(val ms: Long) : Ping
    data class Fail(val error: String) : Ping
    data object Skip : Ping
}

/** A round trip this long is called slow. A computer's goes through the server twice and is sealed and opened on the way. */
const val SLOW_SERVER_MS = 500L
const val SLOW_MACHINE_MS = 1500L

/** The middle one of the samples: one slow packet does not make the line slow. */
fun median(samples: List<Long>): Long = samples.sorted()[samples.size / 2]
