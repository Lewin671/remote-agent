package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AccountCacheTest {
    @Test fun reusedImageIdsAndLateLoadsCannotCrossAccounts() {
        val cache = AccountCache<String>(6)
        val old = Any()
        val next = Any()
        cache.use(old)
        cache.put(old, "session/image", "private old image")
        assertEquals("private old image", cache.get(old, "session/image"))
        cache.clear()
        cache.put(old, "session/image", "late old image")
        assertNull(cache.get(old, "session/image"))
        cache.use(next)
        cache.put(old, "session/image", "late old image")
        assertNull(cache.get(next, "session/image"))
        cache.put(next, "session/image", "new image")
        assertEquals("new image", cache.get(next, "session/image"))
        assertNull(cache.get(old, "session/image"))
        cache.use(old)
        assertNull(cache.get(old, "session/image"))
    }

    @Test fun cacheStillEvictsLeastRecentlyUsedImage() {
        val cache = AccountCache<String>(2)
        val owner = Any()
        cache.use(owner)
        cache.put(owner, "a", "a")
        cache.put(owner, "b", "b")
        cache.get(owner, "a")
        cache.put(owner, "c", "c")
        assertNull(cache.get(owner, "b"))
        assertEquals("a", cache.get(owner, "a"))
    }
}
