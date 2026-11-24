package io.github.lewin671.remoteagent.core

import java.io.File

/** File-only rules, shared by encrypted phone stores and tested without Android. */
object ImageDiskPolicy {
    const val DRAFT_LIMIT = 256L * 1024 * 1024
    const val DEFAULT_LIMIT = 150L * 1024 * 1024

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
}
