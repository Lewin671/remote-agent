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

    companion object {
        private const val PAGE_SIZE = 256
        fun <T> empty(): PagedList<T> = PagedList(emptyList(), 0)
        fun <T> from(values: List<T>): PagedList<T> = empty<T>().append(values)
    }
}
