package io.github.lewin671.remoteagent.core

import java.util.concurrent.TimeUnit
import org.junit.rules.Timeout

/**
 * Per-test hang guard (120 s), distinct from a wall-clock timing assertion:
 * it never validates performance or protocol timing, and only fires when a
 * test waits forever so CI prints the stuck test name and thread stack
 * instead of killing the entire Gradle test task silently at 5 minutes.
 */
fun hangGuard(): Timeout =
    Timeout.builder()
        .withTimeout(120, TimeUnit.SECONDS)
        .withLookingForStuckThread(true)
        .build()
