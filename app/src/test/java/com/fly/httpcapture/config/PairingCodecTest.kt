package com.fly.httpcapture.config

import org.junit.Assert.assertEquals
import org.junit.Test
import java.security.MessageDigest
import java.util.Base64

class PairingCodecTest {
    @Test
    fun decodeV4ReferenceBuildsPinnedHttpsDownloadUrl() {
        val bundle = """{"v":4,"engine":"proxify"}""".toByteArray(Charsets.UTF_8)
        val digest = Base64.getUrlEncoder().withoutPadding()
            .encodeToString(MessageDigest.getInstance("SHA-256").digest(bundle))
        val reference = PairingCodec.decodeReference(
            "httpcapture://p/4/192.168.1.10/39000/0123456789abcdef0123456789abcdef0123456789ab/${"B".repeat(64)}/$digest"
        )

        assertEquals("https://192.168.1.10:39000/p/0123456789abcdef0123456789abcdef0123456789ab", reference.downloadUrl)
        assertEquals("B".repeat(64), reference.controlCertSha256)
        assertEquals(digest, reference.bundleSha256)
    }
}
