package io.github.lewin671.remoteagent.core

import org.junit.Assert.*
import org.junit.Test

class PagedListTest {
    @Test fun appendsAcrossPageBoundariesWithoutMutatingPublishedSnapshots() {
        val values = List(1100) { Any() }
        var list = PagedList.empty<Any>()
        val snapshots = mutableListOf<PagedList<Any>>()
        for (range in listOf(0..0, 1..254, 255..256, 257..999, 1000..1099)) {
            snapshots += list
            list = list.append(values.slice(range))
            assertEquals(values.take(range.last + 1), list)
            for (before in snapshots) {
                assertEquals(values.take(before.size), before)
            }
        }
        assertSame(list, list.append(emptyList()))
        try { list[-1]; fail("must reject negative indexes") } catch (_: IndexOutOfBoundsException) {}
        try { list[1100]; fail("must reject past the end") } catch (_: IndexOutOfBoundsException) {}
    }
}
