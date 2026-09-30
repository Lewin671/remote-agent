package io.github.lewin671.remoteagent.core

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import java.math.BigDecimal

/**
 * InputDigest from internal/agent: hex SHA-256 of a shell call's command,
 * else of the input as Go's encoding/json writes it (compact, keys sorted,
 * Go's escapes and number format). Empty when Go could not read the input.
 * Checked against internal/agent/testdata/digest-vectors.json. It ties an
 * approval request (its tool_input_json) to the tool call it is about
 * (ToolCall.input_digest).
 */
fun inputDigest(tool: String, raw: String): String {
    val v = try {
        Json.parseToJsonElement(raw)
    } catch (e: Exception) {
        return ""
    }
    val text = if (tool == "Bash") {
        // Go unmarshals into {command, description string}: anything else there fails.
        if (v is JsonNull) "" else {
            val o = v as? JsonObject ?: return ""
            for (f in listOf("command", "description")) {
                val x = o[f] ?: continue
                if (x !is JsonNull && !(x is JsonPrimitive && x.isString)) return ""
            }
            (o["command"] as? JsonPrimitive)?.takeIf { it.isString }?.content.orEmpty()
        }
    } else {
        buildString { goJson(v, this) }
    }
    return sha256(text.toByteArray()).hex()
}

private fun goJson(v: JsonElement, out: StringBuilder) {
    when (v) {
        is JsonNull -> out.append("null")
        is JsonObject -> {
            out.append('{')
            // Go sorts map keys by their UTF-8 bytes.
            val keys = v.keys.sortedWith { a, b -> compareUtf8(a, b) }
            keys.forEachIndexed { i, k ->
                if (i > 0) out.append(',')
                goString(k, out)
                out.append(':')
                goJson(v.getValue(k), out)
            }
            out.append('}')
        }
        is JsonArray -> {
            out.append('[')
            v.forEachIndexed { i, x ->
                if (i > 0) out.append(',')
                goJson(x, out)
            }
            out.append(']')
        }
        is JsonPrimitive -> when {
            v.isString -> goString(v.content, out)
            v.content == "true" || v.content == "false" -> out.append(v.content)
            else -> out.append(goNumber(v.content.toDouble()))
        }
    }
}

private fun compareUtf8(a: String, b: String): Int {
    val x = a.toByteArray()
    val y = b.toByteArray()
    for (i in 0 until minOf(x.size, y.size)) {
        val c = (x[i].toInt() and 0xff) - (y[i].toInt() and 0xff)
        if (c != 0) return c
    }
    return x.size - y.size
}

private fun goString(s: String, out: StringBuilder) {
    out.append('"')
    for (c in s) {
        when (c) {
            '"' -> out.append("\\\"")
            '\\' -> out.append("\\\\")
            '\n' -> out.append("\\n")
            '\r' -> out.append("\\r")
            '\t' -> out.append("\\t")
            '\b' -> out.append("\\b")
            '\u000c' -> out.append("\\f")
            '<', '>', '&', ' ', ' ' -> out.append("\\u%04x".format(c.code))
            else -> if (c < ' ') out.append("\\u%04x".format(c.code)) else out.append(c)
        }
    }
    out.append('"')
}

/** A float64 as Go's encoding/json writes it. */
internal fun goNumber(d: Double): String {
    if (d == 0.0) return if (1.0 / d < 0) "-0" else "0"
    val abs = Math.abs(d)
    val dec = BigDecimal(d.toString()).stripTrailingZeros()
    if (abs >= 1e-6 && abs < 1e21) return dec.toPlainString()
    // Shortest digits in exponent form: 1e+21, 1.5e-7.
    val digits = dec.unscaledValue().abs().toString()
    val exp = digits.length - 1 - dec.scale()
    val mant = if (digits.length == 1) digits else digits[0] + "." + digits.substring(1)
    return (if (d < 0) "-" else "") + mant + "e" + (if (exp < 0) "-" else "+") + Math.abs(exp).toString().padStart(if (exp < 0) 1 else 2, '0')
}
