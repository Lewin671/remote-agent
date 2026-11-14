package io.github.lewin671.remoteagent.core

import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test

class DispatchStateTest {
    @Test fun ownershipVectors() {
        val file=File(File(System.getProperty("outcomeVectors")).parentFile,"dispatch.json")
        for (element in Json.parseToJsonElement(file.readText()).jsonArray) {
            val row=element.jsonObject
            val name=row.getValue("name").jsonPrimitive.content
            val only=System.getenv("RA_DISPATCH_CASE")
            if (!only.isNullOrEmpty() && only!=name) continue
            val state=DispatchState(row.getValue("prior").jsonPrimitive.boolean)
            val calls=mutableMapOf<String,DispatchState.Call>()
            val deliveries=mutableMapOf<String,DispatchState.Call.Delivery>()
            for (entry in row.getValue("steps").jsonArray) {
                val step=entry.jsonObject
                fun s(key:String)=step[key]?.jsonPrimitive?.content.orEmpty()
                when(s("op")) {
                    "open" -> calls[s("call")]=state.open(false)
                    "delivery" -> deliveries[s("delivery")]=calls.getValue(s("call")).delivery()
                    "dispatch" -> deliveries.getValue(s("delivery")).dispatch()
                    "unsent" -> deliveries.getValue(s("delivery")).notForwarded()
                    "close" -> calls.getValue(s("call")).close()
                    "check" -> {
                        val call=calls.getValue(s("call"))
                        assertEquals(name,step.getValue("uncertain").jsonPrimitive.boolean,call.uncertain)
                        assertEquals(name,step.getValue("hasPrior").jsonPrimitive.boolean,call.hasPrior)
                    }
                    else -> error(s("op"))
                }
            }
        }
    }
}
