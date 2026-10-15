package io.github.lewin671.remoteagent.term

/**
 * How one line of terminal history (`tmux capture-pane -e`) becomes styled runs
 * (testdata/terminal/history-sgr.json, the contract all three clients read by). Each line
 * is parsed on its own, starting from the default style. Only SGR (`ESC [ ... m`) is
 * applied; every other escape or control sequence is dropped without printing it.
 * Adjacent runs with the same style are merged, and empty runs are omitted.
 */
sealed interface SgrColor {
    /** A 256-colour palette index; 0-15 are the basic and bright colours. */
    data class Palette(val index: Int) : SgrColor

    /** A truecolor value, as `#rrggbb`. */
    data class Rgb(val hex: String) : SgrColor
}

/** A stretch of one history line that shares one style. */
data class SgrRun(
    val text: String,
    val fg: SgrColor? = null,
    val bg: SgrColor? = null,
    val bold: Boolean = false,
    val dim: Boolean = false,
    val italic: Boolean = false,
    val underline: Boolean = false,
    val inverse: Boolean = false,
)

object TerminalSgr {
    fun parse(line: String): List<SgrRun> {
        val runs = ArrayList<SgrRun>()
        var style = Style()
        val text = StringBuilder()
        var i = 0

        fun flush() {
            if (text.isEmpty()) return
            runs += style.run(text.toString())
            text.clear()
        }

        while (i < line.length) {
            val c = line[i]
            if (c != ESC) {
                // A tab and everything printable is kept; the other controls are dropped.
                if (c == '\t' || c >= ' ') text.append(c)
                i++
                continue
            }
            val sequence = sequence(line, i)
            val next = sequence.params?.let { apply(style, it) }
            if (next != null && next != style) {
                flush()
                style = next
            }
            i = sequence.end
        }
        flush()
        return runs
    }
}

private const val ESC = '\u001b'

/** A control sequence to step over: where it ends, and the SGR parameters it carries, if any. */
private class Sequence(val end: Int, val params: String?)

private fun sequence(line: String, start: Int): Sequence {
    return when (line.getOrNull(start + 1)) {
        // CSI: parameters and intermediates, then one final byte.
        '[' -> {
            var i = start + 2
            while (i < line.length && line[i] !in '@'..'~') i++
            if (i == line.length) return Sequence(line.length, null)
            val params = line.substring(start + 2, i)
            Sequence(i + 1, if (line[i] == 'm' && params.all { it in '0'..'9' || it == ';' }) params else null)
        }
        // OSC: ended by BEL, or by ST (ESC \).
        ']' -> {
            var i = start + 2
            while (i < line.length) {
                if (line[i] == '\u0007') return Sequence(i + 1, null)
                if (line[i] == ESC && line.getOrNull(i + 1) == '\\') return Sequence(i + 2, null)
                i++
            }
            Sequence(line.length, null)
        }
        // A two-character escape such as ESC ( B or ESC 7: there is no payload to read.
        else -> Sequence((start + 2).coerceAtMost(line.length), null)
    }
}

/** The style of one run at a time. */
private data class Style(
    val fg: SgrColor? = null,
    val bg: SgrColor? = null,
    val bold: Boolean = false,
    val dim: Boolean = false,
    val italic: Boolean = false,
    val underline: Boolean = false,
    val inverse: Boolean = false,
) {
    fun run(text: String) = SgrRun(text, fg, bg, bold, dim, italic, underline, inverse)
}

/** The style after one SGR sequence; a code that means nothing here leaves the style alone. */
private fun apply(style: Style, params: String): Style {
    val codes = params.split(';')
    var s = style
    var i = 0
    while (i < codes.size) {
        // An empty parameter is a reset, as no parameter at all is.
        val code = if (codes[i].isEmpty()) 0 else codes[i].toIntOrNull() ?: break
        when {
            code == 0 -> s = Style()
            code == 1 -> s = s.copy(bold = true)
            code == 2 -> s = s.copy(dim = true)
            code == 3 -> s = s.copy(italic = true)
            code == 4 -> s = s.copy(underline = true)
            code == 7 -> s = s.copy(inverse = true)
            code == 22 -> s = s.copy(bold = false, dim = false)
            code == 23 -> s = s.copy(italic = false)
            code == 24 -> s = s.copy(underline = false)
            code == 27 -> s = s.copy(inverse = false)
            code == 39 -> s = s.copy(fg = null)
            code == 49 -> s = s.copy(bg = null)
            code in 30..37 -> s = s.copy(fg = SgrColor.Palette(code - 30))
            code in 90..97 -> s = s.copy(fg = SgrColor.Palette(code - 90 + 8))
            code in 40..47 -> s = s.copy(bg = SgrColor.Palette(code - 40))
            code in 100..107 -> s = s.copy(bg = SgrColor.Palette(code - 100 + 8))
            code == 38 || code == 48 -> {
                // A colour that is cut short leaves the style as it was, and nothing after it counts.
                val extended = extended(codes, i + 1) ?: return s
                s = if (code == 38) s.copy(fg = extended.first) else s.copy(bg = extended.first)
                i += extended.second
            }
        }
        i++
    }
    return s
}

/** The colour of a `38`/`48` sequence and how many parameters it used. */
private fun extended(codes: List<String>, from: Int): Pair<SgrColor, Int>? {
    return when (codes.getOrNull(from)?.toIntOrNull()) {
        5 -> byte(codes.getOrNull(from + 1))?.let { SgrColor.Palette(it) to 2 }
        2 -> {
            val r = byte(codes.getOrNull(from + 1))
            val g = byte(codes.getOrNull(from + 2))
            val b = byte(codes.getOrNull(from + 3))
            if (r == null || g == null || b == null) null else SgrColor.Rgb(hex(r, g, b)) to 4
        }
        else -> null
    }
}

/** One colour component: 0-255, or null when the parameter is missing or out of range. */
private fun byte(s: String?): Int? = s?.toIntOrNull()?.takeIf { it in 0..255 }

private fun hex(r: Int, g: Int, b: Int) = "#" + Integer.toHexString(r shl 16 or (g shl 8) or b).padStart(6, '0')
