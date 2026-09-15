package com.fly.httpcapture.config

import org.json.JSONObject
import java.net.HttpURLConnection
import java.util.UUID

data class ControlCaptureResult(
    val captureId: String,
    val status: String,
    val sessionDir: String?,
)

data class ControlStatus(
    val status: String,
    val captureId: String?,
)

object ControlClient {
    fun canControl(profile: CaptureProfile): Boolean =
        !profile.controlBaseUrl.isNullOrBlank() &&
            !profile.controlCertSha256.isNullOrBlank() &&
            !profile.profileId.isNullOrBlank() &&
            !profile.deviceId.isNullOrBlank() &&
            !profile.deviceToken.isNullOrBlank()

    fun start(profile: CaptureProfile, packages: Set<String>, deviceName: String): ControlCaptureResult {
        require(canControl(profile)) { "当前配置不支持 App 联动，请重新用 serve --pair 扫码" }
        val body = JSONObject().apply {
            put("commandId", commandId())
            put("profileId", profile.profileId)
            put("deviceId", profile.deviceId)
            put("deviceName", deviceName)
            put("proxyHost", profile.host)
            put("proxyPort", profile.port)
            put("packages", org.json.JSONArray(packages.sorted()))
        }
        return parseCaptureResult(post(profile, "/control/v1/captures/start", body))
    }

    fun confirm(profile: CaptureProfile, captureId: String): ControlCaptureResult =
        parseCaptureResult(captureCommand(profile, captureId, "vpn-started"))

    fun stop(profile: CaptureProfile, captureId: String): ControlCaptureResult =
        parseCaptureResult(captureCommand(profile, captureId, "stop"))

    fun abandon(profile: CaptureProfile, captureId: String): ControlCaptureResult =
        parseCaptureResult(captureCommand(profile, captureId, "abandon"))

    fun status(profile: CaptureProfile): ControlStatus {
        require(canControl(profile)) { "当前配置不支持 App 联动，请重新用 serve --pair 扫码" }
        val json = request(profile, "GET", "/control/v1/status", null)
        return ControlStatus(
            status = json.optString("status", "unknown"),
            captureId = json.optString("captureId").takeIf(String::isNotBlank),
        )
    }

    private fun captureCommand(profile: CaptureProfile, captureId: String, action: String): JSONObject {
        val body = JSONObject().apply {
            put("commandId", commandId())
            put("captureId", captureId)
        }
        return request(profile, "POST", "/control/v1/captures/$captureId/$action", body)
    }

    private fun post(profile: CaptureProfile, path: String, body: JSONObject): JSONObject =
        request(profile, "POST", path, body)

    private fun request(profile: CaptureProfile, method: String, path: String, body: JSONObject?): JSONObject {
        val base = requireNotNull(profile.controlBaseUrl).trimEnd('/')
        val connection = PairingClient.open(base + path, profile.controlCertSha256).apply {
            requestMethod = method
            doOutput = body != null
            setRequestProperty("Authorization", "Bearer ${profile.deviceToken}")
            if (body != null) setRequestProperty("Content-Type", "application/json")
        }
        val bytes = body?.toString()?.toByteArray(Charsets.UTF_8)
        if (bytes != null) connection.setFixedLengthStreamingMode(bytes.size)
        return try {
            if (bytes != null) connection.outputStream.use { it.write(bytes) }
            val stream = if (connection.responseCode in 200..299) connection.inputStream else connection.errorStream
            val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            val json = if (text.isBlank()) JSONObject() else JSONObject(text)
            require(connection.responseCode == HttpURLConnection.HTTP_OK && json.optBoolean("ok")) {
                json.optString("error", "电脑返回 HTTP ${connection.responseCode}")
            }
            json
        } finally {
            connection.disconnect()
        }
    }

    private fun parseCaptureResult(json: JSONObject): ControlCaptureResult =
        ControlCaptureResult(
            captureId = json.getString("captureId"),
            status = json.optString("status", ""),
            sessionDir = json.optString("sessionDir").takeIf(String::isNotBlank),
        )

    private fun commandId(): String = UUID.randomUUID().toString()
}
