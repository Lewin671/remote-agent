package io.github.lewin671.remoteagent.data

import io.github.lewin671.remoteagent.ui.SealedFiles

/** A store view ends with its account, including work still waiting on IO. */
class FileScopes(private val files: SealedFiles) {
    private var epoch = 0L

    fun invalidate() = files.transaction { epoch++ }

    fun scoped(): SealedFiles = files.transaction {
        val owned = epoch
        object : SealedFiles {
            override fun <T> transaction(work: () -> T): T = files.transaction {
                check(owned == epoch) { "账户已退出" }
                work()
            }
            override fun put(folder: String, name: String, data: ByteArray) = transaction { files.put(folder, name, data) }
            override fun get(folder: String, name: String) = transaction { files.get(folder, name) }
            override fun remove(folder: String, name: String) = transaction { files.remove(folder, name) }
            override fun clearFolder(folder: String) = transaction { files.clearFolder(folder) }
        }
    }
}
