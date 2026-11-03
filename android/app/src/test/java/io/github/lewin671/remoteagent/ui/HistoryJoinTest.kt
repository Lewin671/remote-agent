package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class HistoryJoinTest {
    @get:Rule val ui = createComposeRule()

    private fun message(seq: Long) =
        Event.newBuilder().setSeq(seq).setAgentMessage(AgentMessage.newBuilder().setText("reply $seq")).build()

    private fun rows(seqs: LongRange, version: Long): ChatProjectionWorker.Rows {
        val events = seqs.map(::message)
        return ChatProjectionWorker.Rows(buildChat(events, derive(events), "Computer"), events, version)
    }

    @Test fun rowsInFrontMatchesIos() {
        assertEquals(0, rowsInFront(emptyList(), emptyList()))
        assertEquals(2, rowsInFront(listOf("e9", "e10", "e11"), listOf("e1", "e2", "e8", "e10", "e11", "e12")))
        assertEquals(1, rowsInFront(listOf("e9", "x"), listOf("e1", "e9", "y")))
        assertEquals(1, rowsInFront(listOf("e5", "c7"), listOf("e1", "e5", "s6", "e9")))
        assertEquals(1, rowsInFront(listOf("x"), listOf("e1", "y")))
    }

    @Test fun historyJoinedInFrontIsNotCountedAsNew() {
        val newest = rows(374L..501L, 1)
        val unseen = NewMessages()
        unseen.saw(newest.items.size)
        // The reader scrolls up; the history before the newest page joins along with one reply.
        val joined = rows(1L..502L, 2)
        assertEquals(373, joinedRows(newest, joined))
        unseen.joined(joinedRows(newest, joined))
        assertEquals(1, unseen.count(joined.items.size))
        // Rows added at the end are new; the first rows shown do not join anything.
        val more = rows(1L..505L, 3)
        assertEquals(0, joinedRows(joined, more))
        assertEquals(4, unseen.count(more.items.size))
        assertEquals(0, joinedRows(ChatProjectionWorker.Rows.EMPTY, newest))
        // Back at the bottom, nothing is new.
        unseen.saw(more.items.size)
        assertEquals(0, unseen.count(more.items.size))
    }

    private fun showList(keys: () -> List<String>): () -> LazyListState {
        lateinit var list: LazyListState
        ui.setContent {
            list = rememberLazyListState()
            LazyColumn(state = list, modifier = Modifier.height(400.dp), contentPadding = PaddingValues(top = 6.dp)) {
                items(keys(), key = { it }) { Text(it, Modifier.height(40.dp)) }
            }
        }
        return { list }
    }

    private fun firstKey(list: LazyListState) =
        list.layoutInfo.visibleItemsInfo.first { it.index == list.firstVisibleItemIndex }.key

    @Test fun theReaderKeepsTheirPlaceWhenALongHistoryJoinsInFront() {
        val before = (374..501).map { "e$it" }
        val after = (1..502).map { "e$it" }
        var keys by mutableStateOf(before)
        val list = showList { keys }
        ui.runOnIdle { list().requestScrollToItem(20, 5) }
        ui.runOnIdle { assertEquals("e394", firstKey(list())) }
        ui.runOnIdle {
            list().keepPlace(before, after, rowsInFront(before, after))
            keys = after
        }
        ui.runOnIdle {
            assertEquals("e394", firstKey(list()))
            assertEquals(393, list().firstVisibleItemIndex)
            assertEquals(5, list().firstVisibleItemScrollOffset)
        }
    }

    @Test fun aFollowingListShowsTheEndFromTheFirstLayoutAfterALongJoin() {
        // Opening a session: the newest page is shown at the end, then the history before it joins.
        val before = (374..501).map { "e$it" }
        val after = (1..501).map { "e$it" }
        var keys by mutableStateOf(before)
        val list = showList { keys }
        ui.runOnIdle { list().requestScrollToItem(before.size + 3) }
        ui.runOnIdle { assertFalse(list().canScrollForward) }
        ui.runOnIdle {
            list().requestScrollToItem(after.size + 3)
            keys = after
        }
        ui.runOnIdle {
            assertFalse(list().canScrollForward)
            assertEquals("e501", list().layoutInfo.visibleItemsInfo.last().key)
        }
    }

    @Test fun withoutKeepingItsPlaceTheListLosesItOnALongJoin() {
        // Why keepPlace exists: the list looks for its first row by key only near where it was.
        val before = (374..501).map { "e$it" }
        var keys by mutableStateOf(before)
        val list = showList { keys }
        ui.runOnIdle { list().requestScrollToItem(20, 5) }
        ui.runOnIdle { keys = (1..502).map { "e$it" } }
        ui.runOnIdle { assertNotEquals("e394", firstKey(list())) }
    }
}
