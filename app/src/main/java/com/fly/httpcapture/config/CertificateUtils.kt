package com.fly.httpcapture.config

import android.content.Context
import android.content.ContentValues
import android.content.Intent
import android.security.KeyChain
import android.util.Base64
import android.os.Environment
import android.provider.MediaStore
import java.io.ByteArrayInputStream
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.X509Certificate
import java.security.cert.CertificateFactory

object CertificateUtils {
    fun der(profile: CaptureProfile): ByteArray = Base64.decode(profile.certificateDerBase64, Base64.DEFAULT)

    fun validate(profile: CaptureProfile): X509Certificate {
        val bytes = der(profile)
        val certificate = CertificateFactory.getInstance("X.509")
            .generateCertificate(ByteArrayInputStream(bytes)) as X509Certificate
        val actual = sha256(bytes)
        require(actual.equals(profile.certificateSha256, ignoreCase = true)) { "证书指纹与二维码不一致" }
        require(certificate.basicConstraints >= 0) { "导入的证书不是 CA 证书" }
        return certificate
    }

    fun installIntent(profile: CaptureProfile): Intent {
        validate(profile)
        return KeyChain.createInstallIntent().apply {
            putExtra(KeyChain.EXTRA_CERTIFICATE, der(profile))
            putExtra(KeyChain.EXTRA_NAME, "HTTP Capture - ${profile.name}")
        }
    }

    @android.annotation.TargetApi(android.os.Build.VERSION_CODES.R)
    fun exportToDownloads(context: Context, profile: CaptureProfile): String {
        validate(profile)
        require(android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.R)
        val safeName = profile.name.replace(Regex("[^A-Za-z0-9._-]"), "_").take(40).ifBlank { "proxy" }
        val fileName = "httpcapture-$safeName-${profile.certificateSha256.take(8)}.crt"
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, fileName)
            put(MediaStore.MediaColumns.MIME_TYPE, "application/x-x509-ca-cert")
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/HTTP Capture")
            put(MediaStore.MediaColumns.IS_PENDING, 1)
        }
        val resolver = context.contentResolver
        val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: error("无法创建证书文件")
        try {
            resolver.openOutputStream(uri, "w")?.use { it.write(der(profile)) }
                ?: error("无法写入证书文件")
            values.clear()
            values.put(MediaStore.MediaColumns.IS_PENDING, 0)
            resolver.update(uri, values, null, null)
        } catch (error: Exception) {
            resolver.delete(uri, null, null)
            throw error
        }
        return fileName
    }

    fun isInstalled(context: Context, profile: CaptureProfile): Boolean = runCatching {
        val expected = profile.certificateSha256
        val store = KeyStore.getInstance("AndroidCAStore").apply { load(null) }
        store.aliases().toList().any { alias ->
            val certificate = store.getCertificate(alias) as? X509Certificate ?: return@any false
            sha256(certificate.encoded).equals(expected, ignoreCase = true)
        }
    }.getOrDefault(false)

    fun validityProblem(profile: CaptureProfile): String? = runCatching {
        val certificate = validate(profile)
        val now = java.util.Date()
        when {
            now.before(certificate.notBefore) -> "CA 尚未生效"
            now.after(certificate.notAfter) -> "CA 已过期，请更新 ${profile.name} 的 CA 后再次扫码"
            else -> null
        }
    }.getOrElse { "CA 无效：${it.message}" }

    private fun sha256(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256")
        .digest(bytes).joinToString("") { "%02X".format(it) }
}
