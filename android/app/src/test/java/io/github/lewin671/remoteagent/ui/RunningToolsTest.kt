package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ToolKind
import org.junit.Assert.assertEquals
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class RunningToolsTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()

    @Test
    fun expandedTextUsesOriginalInputAndFullPaths() {
        val kind = ToolKind.TOOL_KIND_COMMAND
        for (key in listOf("command", "CommandLine", "cmd")) {
            assertEquals("cat <<'EOF'\n/work/file\nEOF\n", commandOf(kind, "preview", "{\"$key\":\"cat <<'EOF'\\n/work/file\\nEOF\\n\"}"))
        }
        assertEquals("exec", commandOf(kind, "exec", ""))
        assertEquals("\"printf\"", commandOf(kind, "\"printf\"", "\"printf\""))
        assertEquals("original", commandOf(kind, "original", "{}"))
        assertEquals("\"printf\"", commandOf(kind, "preview", "\"printf\""))
        assertEquals("true", commandOf(kind, "preview", "true"))
        assertEquals("echo /work/file\nnext", commandOf(kind, "preview", "echo /work/file\nnext"))
        assertEquals("/work/full/path", commandOf(ToolKind.TOOL_KIND_FILE_CHANGE, "/work/full/path", "{}"))
    }
}
