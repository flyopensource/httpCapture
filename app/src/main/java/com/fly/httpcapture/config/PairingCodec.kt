package com.fly.httpcapture.config

import org.json.JSONObject
import java.net.URI
import java.security.MessageDigest
import java.util.Base64

data class PairingReference(
    val downloadUrl: String,
    val bundleSha256: String,
    val controlCertSha256: String? = null,
)

object PairingCodec {
    const val MAX_BUNDLE_BYTES = 64 * 1024

    fun decodeReference(value: String): PairingReference {
        val raw = value.trim()
        require(raw.length <= 1024) { "配对二维码数据过长" }
        val uri = runCatching { URI(raw) }.getOrElse { error("不是 HTTP Capture 配对二维码") }
        require(uri.scheme == "httpcapture" && uri.host == "p" && uri.query == null && uri.fragment == null) {
            "不是 HTTP Capture 配对二维码"
        }
        val segments = uri.path.trim('/').split('/')
        require((segments.size == 5 && segments[0] == "3") || (segments.size == 6 && segments[0] == "4")) { "不支持的二维码版本" }
        val version = segments[0]
        val host = segments[1]
        val port = segments[2].toIntOrNull()
        val token = segments[3]
        val controlFingerprint = if (version == "4") segments[4].uppercase() else null
        val digest = if (version == "4") segments[5] else segments[4]
        require(isIpv4(host) && host != "0.0.0.0" && port != null && port in 1..65535) { "临时下载地址无效" }
        require(token.matches(Regex("[A-Za-z0-9_-]{22,128}"))) { "配对会话令牌无效" }
        require(controlFingerprint == null || controlFingerprint.matches(Regex("[0-9A-F]{64}"))) { "控制服务身份指纹无效" }
        val expectedDigest = runCatching { Base64.getUrlDecoder().decode(digest) }.getOrNull()
        require(expectedDigest?.size == 32) { "配对数据指纹无效" }
        val downloadUrl = URI(if (version == "4") "https" else "http", null, host, port, "/p/$token", null, null).toString()
        return PairingReference(downloadUrl, digest, controlFingerprint)
    }

    fun decodeBundle(bytes: ByteArray, expectedSha256: String): CaptureProfile {
        require(bytes.isNotEmpty() && bytes.size <= MAX_BUNDLE_BYTES) { "配对数据大小无效" }
        val actual = MessageDigest.getInstance("SHA-256").digest(bytes)
        val expected = runCatching { Base64.getUrlDecoder().decode(expectedSha256) }.getOrNull()
        require(expected != null && MessageDigest.isEqual(actual, expected)) {
            "配对数据校验失败"
        }
        val json = JSONObject(String(bytes, Charsets.UTF_8))
        val version = json.getInt("v")
        require(version == 3 || version == 4) { "不支持的配置版本" }
        val host = json.getString("host").trim()
        val port = json.getInt("port")
        val certificate = json.getString("certificateDer")
        val certificateSha256 = json.getString("certificateSha256").uppercase()
        val name = json.optString("name", "$host:$port").trim()
        val engine = ProxyEngine.fromWire(json.getString("engine"))
        val controlBaseUrl = json.optString("controlBaseUrl").takeIf(String::isNotBlank)
        val controlCertSha256 = json.optString("controlCertSha256").takeIf(String::isNotBlank)?.uppercase()
        val profileId = json.optString("profileId").takeIf(String::isNotBlank)
        val deviceId = json.optString("deviceId").takeIf(String::isNotBlank)
        val deviceToken = json.optString("deviceToken").takeIf(String::isNotBlank)
        require(host.isNotEmpty() && host.length <= 253 && !host.contains(Regex("[\\s/:]")) && port in 1..65535) { "代理地址无效" }
        require(name.isNotEmpty() && name.length <= 100) { "电脑名称无效" }
        require(certificate.isNotEmpty() && certificateSha256.matches(Regex("[0-9A-F]{64}"))) { "证书信息无效" }
        if (version == 4) {
            require(controlBaseUrl?.startsWith("https://") == true) { "控制服务地址无效" }
            require(controlCertSha256?.matches(Regex("[0-9A-F]{64}")) == true) { "控制服务身份无效" }
            require(!profileId.isNullOrBlank() && !deviceId.isNullOrBlank() && !deviceToken.isNullOrBlank()) { "控制凭据无效" }
        }
        val id = profileId ?: "${engine.wireName}:${certificateSha256.take(16)}@$host:$port"
        return CaptureProfile(
            id = id,
            name = name,
            host = host,
            port = port,
            certificateDerBase64 = certificate,
            certificateSha256 = certificateSha256,
            engine = engine,
            controlBaseUrl = controlBaseUrl,
            controlCertSha256 = controlCertSha256,
            profileId = profileId,
            deviceId = deviceId,
            deviceToken = deviceToken,
        )
    }

    private fun isIpv4(value: String): Boolean {
        val parts = value.split('.')
        return parts.size == 4 && parts.all { part ->
            part.isNotEmpty() && part.length <= 3 && part.all(Char::isDigit) && (part.toIntOrNull() ?: -1) in 0..255
        }
    }

}
