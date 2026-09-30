package io.github.lewin671.remoteagent.push

import androidx.core.app.NotificationCompat
import org.junit.Assert.assertEquals
import org.junit.Test

class DisplayTest {
    @Test
    fun contentOnlyWhenTurnedOnAndUnlocked() {
        assertEquals(Notifier.Display(false, false, NotificationCompat.VISIBILITY_PRIVATE), Notifier.display(false, true, false, 35))
        // Locked: generic, whatever the lock screen settings.
        assertEquals(Notifier.Display(false, false, NotificationCompat.VISIBILITY_PRIVATE), Notifier.display(true, true, true, 35))
        // Unlocked: content, hidden from the lock screen altogether.
        assertEquals(Notifier.Display(true, true, NotificationCompat.VISIBILITY_SECRET), Notifier.display(true, true, false, 35))
        // Below API 31 buttons cannot require unlocking, so there are none.
        assertEquals(Notifier.Display(true, false, NotificationCompat.VISIBILITY_SECRET), Notifier.display(true, true, false, 30))
    }
}
