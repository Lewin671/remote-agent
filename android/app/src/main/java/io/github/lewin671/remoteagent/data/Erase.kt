package io.github.lewin671.remoteagent.data

/** Runs every step, also after one failed, then throws the first failure. */
suspend fun everyStep(vararg steps: suspend () -> Unit) {
    var first: Exception? = null
    for (step in steps) {
        try {
            step()
        } catch (e: Exception) {
            if (first == null) first = e
        }
    }
    first?.let { throw it }
}

/** An account change finishes even when its calling page leaves composition. */
suspend fun <T> finishAccountChange(work: suspend () -> T): T =
    kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) { work() }
