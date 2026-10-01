package io.github.lewin671.remoteagent.core

/** Immutable append snapshots. Appending copies the page table and at most
 * one partial page, rather than every event. Old snapshots never change. */
class PagedList<T> private constructor(private val pages: List<List<T>>, override val size: Int) : AbstractList<T>() {
    override fun get(index: Int): T {
        if (index !in 0 until size) throw IndexOutOfBoundsException("index $index, size $size")
        return pages[index / PAGE_SIZE][index % PAGE_SIZE]
    }

    fun append(values: List<T>): PagedList<T> {
        if (values.isEmpty()) return this
        val next = ArrayList(pages)
        var offset = 0
        if (size % PAGE_SIZE != 0) {
            val last = ArrayList(next.removeAt(next.lastIndex))
            val n = minOf(PAGE_SIZE - last.size, values.size)
            last.addAll(values.subList(0, n))
            next.add(last)
            offset = n
        }
        while (offset < values.size) {
            val end = minOf(offset + PAGE_SIZE, values.size)
            next.add(ArrayList(values.subList(offset, end)))
            offset = end
        }
        return PagedList(next, size + values.size)
    }

    /** Also checks partial pages, so a sibling snapshot with a replaced tail
     * cannot masquerade as an append. Full shared pages need one comparison. */
    fun extends(previous: PagedList<T>): Boolean {
        if (size < previous.size) return false
        for (i in previous.pages.indices) {
            val before = previous.pages[i]
            val after = pages[i]
            if (before === after) continue
            if (before.indices.any { before[it] !== after[it] }) return false
        }
        return true
    }

    companion object {
        private const val PAGE_SIZE = 256
        fun <T> empty(): PagedList<T> = PagedList(emptyList(), 0)
        fun <T> from(values: List<T>): PagedList<T> = empty<T>().append(values)
    }
}

fun <T> unchangedPrefix(events: List<T>, previous: List<T>): Boolean =
    if (events is PagedList<T> && previous is PagedList<T>) events.extends(previous)
    else events.size >= previous.size && previous.indices.all { events[it] === previous[it] }
