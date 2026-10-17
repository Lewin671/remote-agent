package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import kotlinx.serialization.json.*

/** Test-only proto JSON reader for javalite, which has no JsonFormat. Strict
 * reflection uses generated setters, so unknown fields/types fail the test. */
internal fun protoJson(default: MessageLite, json: JsonObject): MessageLite {
    val b = default.newBuilderForType()
    for ((key, value) in json) {
        val suffix = key.replaceFirstChar { it.uppercaseChar() }
        val repeated = value is JsonArray
        val name = (if (repeated) "add" else "set") + suffix
        val all = b.javaClass.methods
        val numeric = all.firstOrNull { it.name == name + "Value" && it.parameterCount == 1 }
        val methods = if (numeric != null) listOf(numeric) else all.filter { it.name == name && it.parameterCount == 1 }
        val setter = methods.firstOrNull { MessageLite::class.java.isAssignableFrom(it.parameterTypes[0]) }
            ?: methods.firstOrNull { !it.parameterTypes[0].isEnum }
            ?: error("Unsupported proto field $key")
        fun argument(v: JsonElement): Any {
            val type = setter.parameterTypes[0]
            if (MessageLite::class.java.isAssignableFrom(type)) {
                val instance = type.getMethod("getDefaultInstance").invoke(null) as MessageLite
                return protoJson(instance, v.jsonObject)
            }
            val p = v.jsonPrimitive
            return when (type) {
                String::class.java -> p.content
                java.lang.Long.TYPE -> p.content.toLong()
                java.lang.Integer.TYPE -> p.int
                java.lang.Boolean.TYPE -> p.boolean
                else -> error("Unsupported proto type $type")
            }
        }
        if (methods.isEmpty()) error("Unknown field $key")
        if (repeated) for (v in value.jsonArray) setter.invoke(b, argument(v)) else setter.invoke(b, argument(value))
    }
    return b.build()
}

