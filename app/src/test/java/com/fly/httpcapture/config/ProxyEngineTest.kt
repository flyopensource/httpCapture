package com.fly.httpcapture.config

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ProxyEngineTest {
    @Test
    fun wireNamesResolveToSupportedEngines() {
        assertEquals(ProxyEngine.PROXIFY, ProxyEngine.fromWire(" Proxify "))
        assertEquals(ProxyEngine.CHARLES, ProxyEngine.fromWire("charles"))
        assertEquals(ProxyEngine.MITMPROXY, ProxyEngine.fromWire(" MITMPROXY "))
        assertEquals(ProxyEngine.CUSTOM, ProxyEngine.fromWire("custom"))
    }

    @Test
    fun missingOrUnknownEngineIsRejected() {
        listOf("", "unknown").forEach { value ->
            assertThrows(IllegalArgumentException::class.java) {
                ProxyEngine.fromWire(value)
            }
        }
    }
}
