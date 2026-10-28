package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Status
import org.junit.Assert.assertEquals
import org.junit.Test

class SessionViewTest {
    @Test
    fun theTerminalGivesWayToTheChatWhenItCannotBeShown() {
        // The computer is away: the cached chat can still be read, the terminal cannot.
        assertEquals(setOf(Status.EXITED, Status.OFFLINE), Status.entries.filterNot(::terminalAvailable).toSet())
    }

    @Test
    fun aPlanOfMoreLinesThanItsPreviewCanBeOpenedHoweverItLaysOut() {
        assertEquals(true, planFolds((1..7).joinToString("\n") { "$it. 步骤" }, 6))
        // One long paragraph is a single line to count: whether it is cut is the layout's to say.
        assertEquals(false, planFolds("先把筛选条件抽成一个 hook，".repeat(40), 6))
    }

    @Test
    fun theEndOfASessionSaysWhyItEnded() {
        assertEquals(
            "电脑上的登录 shell 没能启动 CLI（the login shell could not find codex in PATH）。请检查电脑上的 shell 配置，比如 ~/.zshrc。",
            exitReason("the login shell could not find codex in PATH"),
        )
        assertEquals(
            "电脑上的登录 shell 没能启动 CLI（the login shell did not start the CLI）。请检查电脑上的 shell 配置，比如 ~/.zshrc。",
            exitReason("the login shell did not start the CLI"),
        )
        // The one the daemon ends itself: the shell may be waiting for input.
        assertEquals(
            "电脑上的登录 shell 没能启动 CLI（the login shell did not start the CLI within 1m0s），可能卡在等待输入。请检查电脑上的 shell 配置，比如 ~/.zshrc。",
            exitReason("the login shell did not start the CLI within 1m0s"),
        )
        assertEquals("定时任务运行已结束，会话自动关闭了。", exitReason("stopped", true))
        assertEquals("会话是被手动结束的。", exitReason("stopped", false))
        assertEquals("tmux 会话已经不在了。", exitReason("tmux session gone", true))
        // Every other detail keeps the words the card had before.
        assertEquals("会话是被手动结束的。", exitReason("stopped"))
        assertEquals("tmux 会话已经不在了。", exitReason(""))
        assertEquals("tmux 会话已经不在了。", exitReason("tmux session gone"))
    }
}
