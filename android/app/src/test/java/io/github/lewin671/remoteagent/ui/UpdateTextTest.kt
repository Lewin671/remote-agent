package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Release
import io.github.lewin671.remoteagent.update.UpdateState
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class UpdateTextTest {
    private val release = Release("0.29.0", 82, 4, "remote-agent-v0.29.0.apk", "0".repeat(64), 5_976_883, "")

    @Test
    fun theVersionRowNamesANewerReleaseOnlyWhenThereIsOne() {
        assertNull(updateWord(UpdateState.Unknown))
        assertNull(updateWord(UpdateState.Latest(0)))
        assertNull(updateWord(UpdateState.CheckFailed("x")))
        assertEquals("可更新到 0.29.0", updateWord(UpdateState.Available(release)))
        assertEquals("可更新到 0.29.0", updateWord(UpdateState.DownloadFailed(release, "x")))
        assertEquals("0.29.0 已下载", updateWord(UpdateState.Ready(release, File("x"))))
    }

    @Test
    fun theButtonIsTheNextStep() {
        assertEquals(UpdateStep("下载并安装（5.7 MiB）", true), updateStep(UpdateState.Available(release)))
        assertEquals(UpdateStep("正在下载 2.0 MiB / 5.7 MiB", false), updateStep(UpdateState.Downloading(release, 2_097_152)))
        assertEquals(UpdateStep("安装", true), updateStep(UpdateState.Ready(release, File("x"))))
        assertEquals(UpdateStep("重新下载", true, "下载失败（404）"), updateStep(UpdateState.DownloadFailed(release, "下载失败（404）")))
        assertNull(updateStep(UpdateState.Latest(0)))
    }
}
