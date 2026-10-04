package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** Looking a session up by its ID (architecture §8 「按 ID 取」): when it counts as missing, and what a server that cannot be asked means. */
class SessionLookupTest {
    private class Run(private val answers: List<Any>) {
        val waits = mutableListOf<Long>()
        val reports = mutableListOf<SessionLookup>()
        var asked = 0

        fun go() = runBlocking {
            lookUpSession(
                fetch = {
                    when (val a = answers[asked++]) {
                        is Boolean -> a
                        else -> throw a as Exception
                    }
                },
                wait = { waits += it },
                report = { reports += it },
            )
        }
    }

    @Test
    fun aSessionTheServerHasIsFoundWithoutSayingAnything() {
        val r = Run(listOf(true)).apply { go() }
        assertEquals(1, r.asked)
        assertEquals(emptyList<SessionLookup>(), r.reports)
    }

    @Test
    fun aSessionJustStartedIsNotMissingUntilTheServerSaidSoForTenSeconds() {
        // Not uploaded yet for the first three asks, then there.
        val started = Run(listOf(false, false, false, true)).apply { go() }
        assertEquals(listOf(2_500L, 2_500L, 2_500L), started.waits)
        assertEquals(List(3) { SessionLookup.Reading }, started.reports)
        // Never there: missing after the fifth ask, ten seconds after the first.
        val none = Run(List(5) { false }).apply { go() }
        assertEquals(5, none.asked)
        assertEquals(10_000L, none.waits.sum())
        assertEquals(SessionLookup.Missing, none.reports.last())
    }

    @Test
    fun aServerThatCannotBeAskedIsNoAnswerAndIsAskedAgainEverMoreSlowly() {
        val down = Exception("连不上")
        val r = Run(List(6) { down } + listOf(false, down, true)).apply { go() }
        // Said why each time, never as missing; the waits double up to half a minute and start over after an answer.
        assertEquals(listOf(3_000L, 6_000L, 12_000L, 24_000L, 30_000L, 30_000L, 2_500L, 3_000L), r.waits)
        assertEquals(SessionLookup.Failed("连不上"), r.reports.first())
        assertEquals(0, r.reports.count { it == SessionLookup.Missing })
        // What the pages say meanwhile.
        assertEquals("没能读取这个会话：连不上", lookupNote(SessionLookup.Failed("连不上")))
        assertEquals("找不到这个会话", lookupNote(SessionLookup.Missing))
        assertNull(lookupNote(SessionLookup.Reading))
        assertNull(lookupNote(null))
    }
}
