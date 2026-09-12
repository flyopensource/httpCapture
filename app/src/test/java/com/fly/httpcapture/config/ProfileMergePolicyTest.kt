package com.fly.httpcapture.config

import org.junit.Assert.assertEquals
import org.junit.Test

class ProfileMergePolicyTest {
    @Test
    fun caChangeUpdatesSameNamedComputerAndKeepsStableId() {
        val old = profile(id = "old-id", name = "Office Mac", host = "192.168.1.10", fingerprint = "OLD")
        val incoming = profile(id = "new-id", name = "Office Mac", host = "192.168.1.10", fingerprint = "NEW")

        val result = ProfileMergePolicy.upsert(listOf(old), old.id, incoming)

        assertEquals("old-id", result.activeProfileId)
        assertEquals(listOf(incoming.copy(id = "old-id")), result.profiles)
    }

    @Test
    fun addressChangeUpdatesSameNamedComputer() {
        val old = profile(id = "stable-id", name = "Home PC", host = "192.168.1.20")
        val incoming = profile(id = "changed-id", name = "home pc", host = "192.168.1.99", port = 9999)

        val result = ProfileMergePolicy.upsert(listOf(old), old.id, incoming)

        assertEquals(1, result.profiles.size)
        assertEquals("stable-id", result.profiles.single().id)
        assertEquals("192.168.1.99", result.profiles.single().host)
        assertEquals(9999, result.profiles.single().port)
    }

    @Test
    fun differentComputerNameRemainsSeparate() {
        val office = profile(id = "office-id", name = "Office Mac", host = "192.168.1.10")
        val home = profile(id = "home-id", name = "Home PC", host = "192.168.1.20")

        val result = ProfileMergePolicy.upsert(listOf(office), office.id, home)

        assertEquals(listOf(office, home), result.profiles)
        assertEquals("home-id", result.activeProfileId)
    }

    @Test
    fun duplicateSameNamedProfilesAreCollapsedUsingActiveId() {
        val expired = profile(id = "expired-id", name = "Charles Emulator", fingerprint = "OLD")
        val active = profile(id = "active-id", name = "Charles Emulator", fingerprint = "CURRENT")
        val incoming = profile(id = "incoming-id", name = "Charles Emulator", fingerprint = "NEW")

        val result = ProfileMergePolicy.upsert(listOf(expired, active), active.id, incoming)

        assertEquals("active-id", result.activeProfileId)
        assertEquals(listOf(incoming.copy(id = "active-id")), result.profiles)
    }

    private fun profile(
        id: String,
        name: String,
        host: String = "10.0.2.2",
        port: Int = 8888,
        fingerprint: String = "FINGERPRINT",
    ) = CaptureProfile(
        id = id,
        name = name,
        host = host,
        port = port,
        certificateDerBase64 = "CERT",
        certificateSha256 = fingerprint,
    )
}
