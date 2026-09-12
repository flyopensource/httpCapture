package com.fly.httpcapture.config

data class CaptureProfile(
    val id: String,
    val name: String,
    val host: String,
    val port: Int,
    val certificateDerBase64: String,
    val certificateSha256: String,
)

data class CaptureSettings(
    val profiles: List<CaptureProfile> = emptyList(),
    val activeProfileId: String? = null,
    val selectedPackages: Set<String> = emptySet(),
)
