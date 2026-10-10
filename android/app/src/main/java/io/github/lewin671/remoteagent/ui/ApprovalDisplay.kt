package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ApprovalRequested

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

/** What an approval asks for, as its card, its dock and its page head it. */
fun approvalHead(a: ApprovalRequested): String =
    if (a.command.isNotEmpty()) "请求执行" else "请求权限" + (if (a.toolName.isNotEmpty()) " · ${approvalDisplay(a.toolName)}" else "")

/**
 * What an approval would run or pass, shown with whatever it hides spelled
 * out ([approvalDisplay]). Every place that offers 允许 shows it: a tool that
 * is not a shell command has no command, only its input.
 */
fun approvalBody(a: ApprovalRequested): String = approvalDisplay(a.command.ifEmpty { a.toolInputJson })
