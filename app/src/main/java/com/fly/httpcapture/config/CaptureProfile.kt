package com.fly.httpcapture.config

enum class ProxyEngine(val wireName: String, val displayName: String) {
    CHARLES("charles", "Charles"),
    MITMPROXY("mitmproxy", "mitmproxy"),
    CUSTOM("custom", "自定义代理");

    companion object {
        fun fromWire(value: String): ProxyEngine {
            val normalized = value.trim().lowercase()
            return entries.firstOrNull { it.wireName == normalized }
                ?: throw IllegalArgumentException("不支持的代理类型：$normalized")
        }
    }
}

data class CaptureProfile(
    val id: String,
    val name: String,
    val host: String,
    val port: Int,
    val certificateDerBase64: String,
    val certificateSha256: String,
    val engine: ProxyEngine,
)

data class CaptureSettings(
    val profiles: List<CaptureProfile> = emptyList(),
    val activeProfileId: String? = null,
    val selectedPackages: Set<String> = emptySet(),
)
