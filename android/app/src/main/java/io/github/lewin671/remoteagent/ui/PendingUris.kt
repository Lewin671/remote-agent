package io.github.lewin671.remoteagent.ui

/** Live ownership supplements encrypted drafts/outbox records while their saves are in flight. */
internal object PendingUris {
    private val live = java.util.IdentityHashMap<PendingImage, String>()
    @Synchronized fun retain(entry: PendingImage) { entry.uri?.let { live[entry] = it } }
    @Synchronized fun release(entry: PendingImage) { live.remove(entry) }
    @Synchronized fun uris(): Set<String> = live.filterKeys { it.ownerAccount === AccountMemory.token }.values.toSet()
    /** Serialize the last ownership check with a new pick acquiring its persistent grant. */
    @Synchronized fun withoutReference(uri: String, work: () -> Unit) {
        if (live.none { (entry, source) -> source == uri && entry.ownerAccount === AccountMemory.token }) work()
    }
    @Synchronized fun clear() { live.clear() }
}

internal fun unusedUriPermissions(grants: Set<String>, live: Set<String>, recorded: Set<String>?): Set<String> =
    if (recorded == null) emptySet() else grants - live - recorded
