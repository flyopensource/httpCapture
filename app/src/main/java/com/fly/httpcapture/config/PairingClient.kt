package com.fly.httpcapture.config

import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

object PairingClient {
    fun download(reference: PairingReference): CaptureProfile {
        val connection = open(reference.downloadUrl, reference.controlCertSha256).apply { requestMethod = "GET" }
        try {
            val status = connection.responseCode
            require(status == HttpURLConnection.HTTP_OK) { "电脑返回 HTTP $status" }
            val declaredSize = connection.contentLengthLong
            require(declaredSize in -1..PairingCodec.MAX_BUNDLE_BYTES.toLong()) { "电脑返回的配对数据过大" }
            val bytes = connection.inputStream.use(::readLimited)
            return PairingCodec.decodeBundle(bytes, reference.bundleSha256)
        } finally {
            connection.disconnect()
        }
    }

    fun acknowledge(reference: PairingReference) {
        val connection = open(reference.downloadUrl, reference.controlCertSha256).apply {
            requestMethod = "POST"
            setFixedLengthStreamingMode(0)
        }
        try {
            require(connection.responseCode == HttpURLConnection.HTTP_NO_CONTENT) { "电脑未确认导入完成" }
        } finally {
            connection.disconnect()
        }
    }

    internal fun open(value: String, pinnedCertSha256: String? = null): HttpURLConnection =
        (URL(value).openConnection() as HttpURLConnection).apply {
            if (this is HttpsURLConnection && !pinnedCertSha256.isNullOrBlank()) {
                sslSocketFactory = pinnedContext(pinnedCertSha256).socketFactory
                hostnameVerifier = HostnameVerifier { _, _ -> true }
            }
        connectTimeout = 8_000
        readTimeout = 8_000
        instanceFollowRedirects = false
        useCaches = false
        setRequestProperty("Accept", "application/json")
    }

    private fun readLimited(input: java.io.InputStream): ByteArray {
        val output = ByteArrayOutputStream()
        val buffer = ByteArray(8 * 1024)
        while (true) {
            val count = input.read(buffer)
            if (count < 0) break
            require(output.size() + count <= PairingCodec.MAX_BUNDLE_BYTES) { "电脑返回的配对数据过大" }
            output.write(buffer, 0, count)
        }
        return output.toByteArray()
    }

    private fun pinnedContext(expectedSha256: String): SSLContext {
        val trustManager = object : X509TrustManager {
            override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = Unit
            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
                val certificate = chain?.firstOrNull() ?: throw IllegalArgumentException("控制服务未返回证书")
                val digest = MessageDigest.getInstance("SHA-256").digest(certificate.encoded).toHex()
                require(digest.equals(expectedSha256, ignoreCase = true)) { "控制服务身份校验失败" }
            }
        }
        return SSLContext.getInstance("TLS").apply {
            init(null, arrayOf(trustManager), null)
        }
    }

    internal fun ByteArray.toHex(): String = joinToString("") { "%02X".format(it.toInt() and 0xFF) }
}
