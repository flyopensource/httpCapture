package com.fly.httpcapture.config

import android.net.Uri
import android.util.Base64
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.util.zip.GZIPInputStream

object PairingCodec {
    fun decode(value: String): CaptureProfile {
        val uri = Uri.parse(value.trim())
        require(uri.scheme == "httpcapture" && uri.host == "pair") { "不是 HTTP Capture 配对二维码" }
        val segments = uri.pathSegments
        require(segments.size == 2 && segments[0] == "v1") { "不支持的二维码版本" }
        val compressed = Base64.decode(segments[1], Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
        val jsonText = GZIPInputStream(ByteArrayInputStream(compressed)).bufferedReader().use { it.readText() }
        val json = JSONObject(jsonText)
        require(json.getInt("v") == 1) { "不支持的配置版本" }
        val host = json.getString("host").trim()
        val port = json.getInt("port")
        val certificate = json.getString("certificateDer")
        val fingerprint = json.getString("certificateSha256").uppercase()
        require(host.isNotEmpty() && port in 1..65535) { "代理地址无效" }
        require(certificate.isNotEmpty() && fingerprint.matches(Regex("[0-9A-F]{64}"))) { "证书信息无效" }
        val id = "${fingerprint.take(16)}@$host:$port"
        return CaptureProfile(
            id = id,
            name = json.optString("name", "$host:$port"),
            host = host,
            port = port,
            certificateDerBase64 = certificate,
            certificateSha256 = fingerprint,
        )
    }
}
