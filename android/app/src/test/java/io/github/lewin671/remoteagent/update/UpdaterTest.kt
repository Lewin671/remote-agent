package io.github.lewin671.remoteagent.update

import io.github.lewin671.remoteagent.core.Release
import java.io.File
import java.net.SocketTimeoutException
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** Looking again for a newer release while one is already known (client-v42). */
class UpdaterTest {
    private val installed = 1L
    private val old = Release("0.0.2", 2, 4, "remote-agent-v0.0.2.apk", "0".repeat(64), 1024, "更新说明")
    private val newer = old.copy(version = "0.0.3", versionCode = 3, apk = "remote-agent-v0.0.3.apk")
    private val ready = UpdateState.Ready(old, File("old.apk"))
    private val beforeState = Updater.state.value
    private val beforeCheck = Updater.checking.value
    /** An hour, against [Updater.due]'s hours-long interval. */
    private val hour = 3600_000L

    @After fun restore() = Updater.showForTest(beforeState, beforeCheck)

    @Test fun aLookThatFindsAnotherReleaseReplacesWhatIsOnScreen() = runTest {
        for (state in listOf(UpdateState.Available(old), ready, UpdateState.DownloadFailed(old, "连接超时"))) {
            Updater.showForTest(state)
            var cleared = false
            Updater.refresh(true, installed, { newer }) { cleared = true }
            // Offer 0.0.3 instead of installing the downloaded 0.0.2; leave its file for a system installer already reading it.
            assertEquals(UpdateState.Available(newer), Updater.state.value)
            assertFalse("the installer may still be reading the replaced APK", cleared)
            assertEquals(UpdateCheck(), Updater.checking.value)
        }
    }

    @Test fun aLookKeepsTheReleaseOnScreenWhileItRuns() = runTest {
        Updater.showForTest(ready)
        var seen = 0
        Updater.refresh(true, installed, {
            assertEquals(ready, Updater.state.value)
            assertEquals(UpdateCheck(running = true), Updater.checking.value)
            seen++
            old
        }) { fail("the same release keeps the APK that was verified") }
        assertEquals(1, seen)
        assertEquals(ready, Updater.state.value)
        assertEquals(UpdateCheck(upToDate = true), Updater.checking.value)
    }

    @Test fun theSameReleaseKeepsItsVerifiedApkAndSaysSo() = runTest {
        for (state in listOf(UpdateState.Available(old), ready)) {
            Updater.showForTest(state)
            Updater.refresh(true, installed, { old }) { fail("the same release keeps the APK that was verified") }
            assertEquals(state, Updater.state.value)
            assertEquals(UpdateCheck(upToDate = true), Updater.checking.value)
        }
    }

    @Test fun lookingAgainAndAgainKeepsTheSameReleaseAndItsApk() = runTest {
        Updater.showForTest(ready)
        repeat(2) { look ->
            Updater.refresh(true, installed, { old }) { fail("look ${look + 1} let go of the verified APK") }
            assertEquals(ready, Updater.state.value)
            assertEquals(UpdateCheck(upToDate = true), Updater.checking.value)
        }
        // The answer of the look before says nothing while the next one runs.
        Updater.refresh(true, installed, {
            assertEquals(UpdateCheck(running = true), Updater.checking.value)
            old
        }) { fail("the same release keeps the APK that was verified") }
        assertEquals(UpdateCheck(upToDate = true), Updater.checking.value)
    }

    @Test fun aFailedLookKeepsWhatIsKnownAndOnlySaysWhyWhenAskedFor() = runTest {
        Updater.showForTest(ready)
        // Nobody asked for this one: it says nothing, and the release stands.
        Updater.refresh(false, installed, { throw SocketTimeoutException() }) { fail("nothing to clear") }
        assertEquals(ready, Updater.state.value)
        assertEquals(UpdateCheck(), Updater.checking.value)
        // The user asked: the same release is still there, with the reason and a button that works again.
        Updater.refresh(true, installed, { throw SocketTimeoutException() }) { fail("nothing to clear") }
        assertEquals(ready, Updater.state.value)
        assertEquals(UpdateCheck(error = "连接超时"), Updater.checking.value)
        // Trying again can come back with the same release.
        Updater.refresh(true, installed, { old }) { fail("nothing to clear") }
        assertEquals(ready, Updater.state.value)
        assertEquals(UpdateCheck(upToDate = true), Updater.checking.value)
    }

    @Test fun aFirstLookThatFailsLeavesNothingKnownAndKeepsTheReason() = runTest {
        // Nobody asked, nothing is known: the failure is not heard at all.
        Updater.showForTest(UpdateState.Unknown)
        Updater.refresh(false, installed, { throw SocketTimeoutException() }) { fail("nothing to clear") }
        assertEquals(UpdateState.Unknown, Updater.state.value)
        assertEquals(UpdateCheck(), Updater.checking.value)
        // The user asked: the reason is kept, for the sheet to show and to try again after.
        var cleared = false
        Updater.refresh(true, installed, { throw SocketTimeoutException() }) { cleared = true }
        assertEquals(UpdateState.Unknown, Updater.state.value)
        assertFalse(cleared)
        assertEquals(UpdateCheck(error = "连接超时"), Updater.checking.value)
    }

    @Test fun aLookThatFindsThisBuildTheLatestClearsDownloads() = runTest {
        Updater.showForTest(UpdateState.Available(old))
        var cleared = false
        Updater.refresh(true, 99, { old }) { cleared = true }
        assertTrue(cleared)
        assertTrue(Updater.state.value is UpdateState.Latest)
        // Nothing is left to install, so nothing is confirmed as the latest release on screen.
        assertEquals(UpdateCheck(), Updater.checking.value)
    }

    @Test fun aFirstLookKeepsWhateverAnEarlierRunDownloaded() = runTest {
        Updater.showForTest(UpdateState.Unknown)
        var cleared = false
        Updater.refresh(false, installed, { old }) { cleared = true }
        // Nothing was on screen to replace, and a file that matches the manifest is not downloaded again.
        assertFalse(cleared)
        assertEquals(UpdateState.Available(old), Updater.state.value)
        assertEquals(UpdateCheck(), Updater.checking.value)
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun aLookTheAppIsTakenOutOfLeavesNothingRunning() = runTest {
        Updater.showForTest(UpdateState.Available(old))
        val look = launch { Updater.refresh(true, installed, { awaitCancellation() }) { fail("nothing to clear") } }
        runCurrent()
        assertEquals(UpdateCheck(running = true), Updater.checking.value)
        look.cancelAndJoin()
        assertEquals(UpdateCheck(), Updater.checking.value)
        assertEquals(UpdateState.Available(old), Updater.state.value)
    }

    @Test fun theUsersLookIsNeverTooSoonAndTheAutomaticOneWaitsBetweenThem() {
        val now = 1_000_000_000_000L
        // Asked for by the user: it happens on the spot.
        assertTrue(Updater.due(manual = true, now = now, lastAtMs = now))
        assertTrue(Updater.due(manual = true, now = now, lastAtMs = now - hour))
        // Automatic: only after the interval, and one that never ran is due.
        assertFalse(Updater.due(manual = false, now = now, lastAtMs = now - 5 * hour))
        assertTrue(Updater.due(manual = false, now = now, lastAtMs = now - 6 * hour))
        assertTrue(Updater.due(manual = false, now = now, lastAtMs = 0))
    }
}
