package com.fly.httpcapture.vpn

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import com.fly.httpcapture.MainActivity
import com.fly.httpcapture.R
import com.fly.httpcapture.config.ConfigStore
import com.github.shadowsocks.bg.Tun2proxy
import java.net.InetSocketAddress
import java.net.Socket
import kotlin.concurrent.thread

class HttpCaptureVpnService : VpnService() {
    private var vpnInterface: ParcelFileDescriptor? = null
    private var worker: Thread? = null
    @Volatile private var starting = false

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Log.i(TAG, "onStartCommand action=${intent?.action}")
        if (intent?.action == ACTION_STOP) {
            stopCapture()
            return START_NOT_STICKY
        }
        if (!VpnState.running && !starting) {
            starting = true
            startBootstrapForeground()
            thread(name = "vpn-bootstrap", start = true) { startCapture() }
        }
        return START_NOT_STICKY
    }

    private fun startCapture() {
        val settings = ConfigStore(this).load()
        val profile = settings.profiles.firstOrNull { it.id == settings.activeProfileId }
        if (profile == null || settings.selectedPackages.isEmpty()) {
            Log.e(TAG, "Missing profile or packages")
            VpnState.update(this, false, "请先导入代理配置并选择应用")
            starting = false
            stopSelf()
            return
        }
        val reachable = runCatching {
            Socket().use { it.connect(InetSocketAddress(profile.host, profile.port), 1_500) }
        }.isSuccess
        if (!reachable) {
            Log.e(TAG, "Proxy is unreachable at ${profile.host}:${profile.port}")
            VpnState.update(this, false, "无法连接代理：${profile.host}:${profile.port}")
            starting = false
            stopSelf()
            return
        }

        val builder = Builder()
            .setSession("HTTP Capture · ${profile.name}")
            .setMtu(MTU)
            .addAddress("10.7.0.1", 32)
            .addAddress("fd00:1:fd00:1::1", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("198.18.0.1")
            .setBlocking(true)
        try {
            settings.selectedPackages.forEach(builder::addAllowedApplication)
            vpnInterface = builder.establish() ?: error("系统未建立 VPN")
        } catch (error: Exception) {
            Log.e(TAG, "VPN establish failed", error)
            VpnState.update(this, false, "VPN 建立失败：${error.message}")
            starting = false
            stopSelf()
            return
        }

        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_capture)
            .setContentTitle("HTTP Capture 正在抓包")
            .setContentText("${settings.selectedPackages.size} 个应用 → ${profile.host}:${profile.port}")
            .setOngoing(true)
            .setContentIntent(PendingIntent.getActivity(this, 1, Intent(this, MainActivity::class.java), pendingFlags()))
            .addAction(0, "停止", PendingIntent.getService(this, 2, stopIntent(this), pendingFlags()))
            .build()
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification)

        starting = false
        VpnState.update(this, true)
        val fd = vpnInterface!!.detachFd()
        vpnInterface = null
        starting = false
        val host = if (profile.host.contains(':')) "[${profile.host}]" else profile.host
        val args = listOf(
            "tun2proxy-bin", "--tun-fd", fd.toString(), "--close-fd-on-drop", "true",
            "--proxy", "http://$host:${profile.port}", "--dns", "virtual",
            "--ipv6-enabled",
            "--mtu", MTU.toString(), "--tcp-mss", "1460", "--verbosity", "info", "--max-sessions", "512",
        ).joinToString(" ")
        worker = thread(name = "tun2proxy", start = true) {
            Log.i(TAG, "Starting tun2proxy with fd=$fd")
            val result = runCatching { Tun2proxy.run(args, MTU.toChar()) }
            val message = result.exceptionOrNull()?.let { "转发内核异常：${it.message}" }
                ?: if (result.getOrDefault(0) != 0) "转发内核退出：${result.getOrDefault(-1)}" else null
            if (result.isSuccess && result.getOrNull() == 0) {
                Log.i(TAG, "tun2proxy stopped normally")
            } else {
                Log.e(TAG, "tun2proxy returned ${result.getOrNull()}", result.exceptionOrNull())
            }
            if (VpnState.running) VpnState.update(this, false, message)
            stopSelf()
        }
    }

    private fun stopCapture() {
        Log.i(TAG, "Stopping capture")
        runCatching { Tun2proxy.stop() }
        runCatching { vpnInterface?.close() }
        vpnInterface = null
        VpnState.update(this, false)
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        if (VpnState.running) stopCapture()
        super.onDestroy()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            getSystemService(NotificationManager::class.java).createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "抓包状态", NotificationManager.IMPORTANCE_LOW)
            )
        }
    }

    private fun startBootstrapForeground() {
        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_capture)
            .setContentTitle("HTTP Capture 正在启动")
            .setContentText("正在检查代理并建立按应用 VPN")
            .setOngoing(true)
            .setContentIntent(PendingIntent.getActivity(this, 1, Intent(this, MainActivity::class.java), pendingFlags()))
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun pendingFlags(): Int = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE

    companion object {
        private const val TAG = "HttpCaptureVpn"
        const val ACTION_START = "com.fly.httpcapture.START"
        const val ACTION_STOP = "com.fly.httpcapture.STOP"
        private const val CHANNEL_ID = "capture_status"
        private const val NOTIFICATION_ID = 1001
        private const val MTU = 1500

        fun startIntent(context: android.content.Context) = Intent(context, HttpCaptureVpnService::class.java).setAction(ACTION_START)
        fun stopIntent(context: android.content.Context) = Intent(context, HttpCaptureVpnService::class.java).setAction(ACTION_STOP)
    }
}
