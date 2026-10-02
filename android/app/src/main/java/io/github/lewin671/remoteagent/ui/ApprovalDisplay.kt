package io.github.lewin671.remoteagent.ui

/** Show approval display controls as escapes; never rewrite the actual payload. */
fun approvalDisplay(text: String): String = buildString {
    for (char in text) {
        val code = char.code
        if ((code < 32 && char != '\n' && char != '\t') || code in 127..159 ||
            code == 0x061c || code in 0x200e..0x200f || code in 0x202a..0x202e || code in 0x2066..0x2069) {
            append("\\u").append(code.toString(16).padStart(4, '0'))
        } else append(char)
    }
}
