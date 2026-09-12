package com.fly.httpcapture.config

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ProfileAddressPolicyTest {
    @Test
    fun parseTrimsHostAndAcceptsPortRange() {
        assertEquals(ProfileAddress("192.168.1.20", 8888), ProfileAddressPolicy.parse(" 192.168.1.20 ", "8888"))
        assertEquals(ProfileAddress("2001:db8::1", 443), ProfileAddressPolicy.parse("[2001:db8::1]", "443"))
    }

    @Test
    fun parseRejectsInvalidHostAndPort() {
        listOf(
            "" to "8888",
            "http://192.168.1.20" to "8888",
            "192.168.1.20/path" to "8888",
            "10.10.0.9999" to "8888",
            "192.168.1" to "8888",
            "192.168.1.20" to "0",
            "192.168.1.20" to "65536",
            "192.168.1.20" to "abc",
        ).forEach { (host, port) ->
            assertThrows("host=$host port=$port", IllegalArgumentException::class.java) {
                ProfileAddressPolicy.parse(host, port)
            }
        }
    }

    @Test
    fun updateOnlyChangesAddressAndKeepsCertificateIdentity() {
        val profile = CaptureProfile(
            id = "stable-id",
            name = "Office Charles",
            host = "192.168.1.10",
            port = 8888,
            certificateDerBase64 = "CERT",
            certificateSha256 = "FINGERPRINT",
            engine = ProxyEngine.CHARLES,
        )

        val updated = ProfileAddressPolicy.update(
            listOf(profile),
            profile.id,
            ProfileAddress("192.168.1.99", 9999),
        ).single()

        assertEquals(profile.copy(host = "192.168.1.99", port = 9999), updated)
    }

    @Test
    fun updateRejectsMissingProfile() {
        assertThrows(IllegalArgumentException::class.java) {
            ProfileAddressPolicy.update(emptyList(), "missing", ProfileAddress("10.0.2.2", 8888))
        }
    }
}
