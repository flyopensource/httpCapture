package com.fly.httpcapture.config

import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL

object PairingClient {
    fun download(reference: PairingReference): CaptureProfile {
        val connection = open(reference.downloadUrl).apply { requestMethod = "GET" }
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
        val connection = open(reference.downloadUrl).apply {
            requestMethod = "POST"
            setFixedLengthStreamingMode(0)
        }
        try {
            require(connection.responseCode == HttpURLConnection.HTTP_NO_CONTENT) { "电脑未确认导入完成" }
        } finally {
            connection.disconnect()
        }
    }

    private fun open(value: String): HttpURLConnection = (URL(value).openConnection() as HttpURLConnection).apply {
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
}
