package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Release
import io.github.lewin671.remoteagent.update.UpdateCheck
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
        assertEquals("可更新到 0.29.0", updateWord(UpdateState.Available(release)))
        assertEquals("可更新到 0.29.0", updateWord(UpdateState.DownloadFailed(release, "x")))
        assertEquals("0.29.0 已下载", updateWord(UpdateState.Ready(release, File("x"))))
    }

    @Test
    fun onlyThisAppsPackageOfThePromisedVersionGoesToTheInstaller() {
        val id = "io.github.lewin671.remoteagent"
        assertNull(io.github.lewin671.remoteagent.update.Updater.packageMismatch(id, id, 82, release))
        // Another app's package would be installed next to this one, unsigned by us.
        assertEquals("下载的安装包不是 remote-agent", io.github.lewin671.remoteagent.update.Updater.packageMismatch(id, "$id.evil", 82, release))
        assertEquals("下载的安装包不是 remote-agent", io.github.lewin671.remoteagent.update.Updater.packageMismatch(id, null, 82, release))
        assertEquals("下载的安装包和发布信息不一致", io.github.lewin671.remoteagent.update.Updater.packageMismatch(id, id, 81, release))
    }

    @Test
    fun theLineUnderAReleaseSaysWhyALookFailedOrThatItIsStillTheOne() {
        // Nothing to say while a look runs, and nothing until one is answered.
        assertNull(checkLine(UpdateCheck(), releaseOnScreen = false))
        assertNull(checkLine(UpdateCheck(), releaseOnScreen = true))
        assertNull(checkLine(UpdateCheck(running = true), releaseOnScreen = true))
        // The same release found again, with it on screen: that is what "最新发布版本" is about.
        assertEquals(CheckLine("已是最新发布版本", error = false), checkLine(UpdateCheck(upToDate = true), releaseOnScreen = true))
        // With no release on screen, the installed build being the latest one is said by the state instead.
        assertNull(checkLine(UpdateCheck(upToDate = true), releaseOnScreen = false))
        // A look the user asked for that failed says why, and outranks anything else.
        assertEquals(CheckLine("没能检查更新：连接超时", error = true), checkLine(UpdateCheck(error = "连接超时"), releaseOnScreen = true))
        assertEquals(CheckLine("没能检查更新：连接超时", error = true), checkLine(UpdateCheck(error = "连接超时"), releaseOnScreen = false))
    }

    @Test
    fun theButtonIsTheNextStep() {
        assertEquals(UpdateStep("下载并安装（5.7 MiB）", true), updateStep(UpdateState.Available(release)))
        assertEquals(UpdateStep("正在下载 2.0 MiB / 5.7 MiB", false), updateStep(UpdateState.Downloading(release, 2_097_152)))
        assertEquals(UpdateStep("安装", true), updateStep(UpdateState.Ready(release, File("x"))))
        assertEquals(UpdateStep("重新下载", true, "下载失败（404）"), updateStep(UpdateState.DownloadFailed(release, "下载失败（404）")))
        assertNull(updateStep(UpdateState.Latest(0)))
    }
    @Test fun pendingInstallWaitsForCheckingAndNeverSwitchesToAnotherRelease() {
        val ready = UpdateState.Ready(release, File("ready.apk"))
        assertNull(readyForInstall(ready, UpdateCheck(running = true), release.versionCode))
        assertEquals(ready, readyForInstall(ready, UpdateCheck(upToDate = true), release.versionCode))
        assertEquals(ready, readyForInstall(ready, UpdateCheck(error = "连接超时"), release.versionCode))
        assertNull(readyForInstall(UpdateState.Available(release.copy(versionCode = 83)), UpdateCheck(), release.versionCode))
        assertNull(readyForInstall(ready.copy(release = release.copy(versionCode = 83)), UpdateCheck(), release.versionCode))
    }

}
