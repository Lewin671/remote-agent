package io.github.lewin671.remoteagent

import java.io.ByteArrayOutputStream
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RoutesTest {
    /** What Navigation does to a path argument: URI percent-decoding, where "+" stays "+". */
    private fun navDecode(s: String): String {
        val out = ByteArrayOutputStream()
        var i = 0
        while (i < s.length) {
            if (s[i] == '%') {
                out.write(s.substring(i + 1, i + 3).toInt(16))
                i += 3
            } else {
                out.write(s[i].code)
                i++
            }
        }
        return out.toString(Charsets.UTF_8)
    }

    @Test
    fun projectPathsSurviveTheRoute() {
        for (path in listOf("/work/C++", "/a/100% done", "/a/50%2B", "/home/me/项目 一", "/x/a?b&c=d#e", "/t/~.-_", "C:\\\\work\\\\a+b")) {
            val arg = routeArg(path)
            // One path segment, with nothing Navigation would read as a query.
            assertTrue(arg, arg.none { it in "/?&#+ " })
            assertEquals(path, routeValue(navDecode(arg)))
        }
    }
}
