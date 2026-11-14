package io.github.lewin671.remoteagent.core

/** Release every owned resource, then report the first failure to the caller. */
internal fun cleanup(vararg steps: () -> Unit) {
    var failure: Throwable? = null
    for (step in steps) {
        try { step() }
        catch (error: Throwable) {
            if (failure == null) failure = error
            else if (failure !== error) failure.addSuppressed(error)
        }
    }
    failure?.let { throw it }
}
