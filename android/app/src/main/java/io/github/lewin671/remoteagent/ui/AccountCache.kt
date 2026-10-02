package io.github.lewin671.remoteagent.ui

/** Plaintext belongs to one active owner. Old asynchronous loads cannot
 * repopulate the cache after logout, even when identifiers are reused. */
class AccountCache<T>(private val limit: Int) {
    private var owner: Any? = null
    private val entries = LinkedHashMap<String, T>(limit, 0.75f, true)

    @Synchronized fun use(owner: Any) {
        if (this.owner !== owner) clear()
        this.owner = owner
    }

    @Synchronized fun get(owner: Any, key: String): T? =
        if (this.owner === owner) entries[key] else null

    @Synchronized fun put(owner: Any, key: String, value: T) {
        if (this.owner !== owner) return
        entries[key] = value
        while (entries.size > limit) entries.remove(entries.keys.first())
    }

    @Synchronized fun clear() {
        entries.clear()
        owner = null
    }
}
