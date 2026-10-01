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
                assertTrue(list.extends(before))
                assertEquals(values.take(before.size), before)
            }
        }
        assertSame(list, list.append(emptyList()))
        assertFalse(list.extends(PagedList.from(List(1101) { Any() })))
    }

    @Test fun replacingATailOrAppendingADifferentSiblingIsNotAnUnchangedPrefix() {
        val original = List(700) { Any() }
        val common = PagedList.from(original.take(300))
        val left = common.append(original.subList(300, 700))
        val sibling = common.append(List(400) { Any() })
        assertFalse(left.extends(sibling))
        assertFalse(sibling.extends(left))
        assertTrue(unchangedPrefix(left, common))
        assertFalse(unchangedPrefix(original.drop(1), original.take(300)))
        try { left[-1]; fail("must reject negative indexes") } catch (_: IndexOutOfBoundsException) {}
        try { left[700]; fail("must reject past the end") } catch (_: IndexOutOfBoundsException) {}
    }
}
