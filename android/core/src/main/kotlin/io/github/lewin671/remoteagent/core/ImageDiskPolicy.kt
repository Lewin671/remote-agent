package io.github.lewin671.remoteagent.core

import java.io.File

/** File-only rules, shared by encrypted phone stores and tested without Android. */
object ImageDiskPolicy {
    const val DRAFT_LIMIT = 256L * 1024 * 1024
    const val DEFAULT_LIMIT = 150L * 1024 * 1024
    const val DRAFT_TTL = 7L * 24 * 60 * 60 * 1000

    fun bytes(dir: File): Long = dir.walkTopDown().filter { it.isFile }.sumOf { it.length() }

    fun trimCache(dir: File, limit: Long): Long {
        val files = dir.listFiles()?.filter { it.isFile }?.sortedBy { it.lastModified() }.orEmpty()
        var total = files.sumOf { it.length() }
        for (f in files) {
            if (total <= limit) break
            val size = f.length()
            if (f.delete()) total -= size
        }
        return total
    }

    /**
     * Drafts are user work: expire by inactivity, refuse new storage at quota,
     * never LRU-delete them. The folders named in [keep] never expire: those
     * of the messages the outbox still waits on.
     */
    fun expireDrafts(dir: File, keep: Set<String> = emptySet(), now: Long = System.currentTimeMillis()): Int {
        var removed = 0
        for (folder in dir.listFiles()?.filter { it.isDirectory }.orEmpty()) {
            if (folder.name in keep) continue
            val files = folder.listFiles().orEmpty()
            val last = maxOf(folder.lastModified(), files.maxOfOrNull { it.lastModified() } ?: 0L)
            if (now - last < DRAFT_TTL) continue
            val count = files.count { it.extension == "meta" }
            if (folder.deleteRecursively()) removed += count
        }
        return removed
    }

    /** The draft keys of the messages an outbox index names, one "session<TAB>request" a line. */
    fun outboxKeys(index: String): List<String> = index.lineSequence().mapNotNull { line ->
        val parts = line.split("\t")
        if (parts.size != 2) null else "out:${parts[0]}:${parts[1]}"
    }.toList()

    fun requireRoom(dir: File, replacing: File, incoming: Long, limit: Long) {
        require(bytes(dir) - replacing.length() + incoming <= limit) { "手机待发图片空间不足，请发送或移除旧草稿" }
    }
}
