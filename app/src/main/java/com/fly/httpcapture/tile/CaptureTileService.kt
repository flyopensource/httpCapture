package com.fly.httpcapture.tile

import android.app.PendingIntent
import android.annotation.SuppressLint
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import androidx.core.content.ContextCompat
import com.fly.httpcapture.MainActivity
import com.fly.httpcapture.config.CaptureProfile
import com.fly.httpcapture.config.ControlCaptureResult
import com.fly.httpcapture.config.ConfigStore
import com.fly.httpcapture.config.ControlClient
import com.fly.httpcapture.config.ControlStatus
import com.fly.httpcapture.vpn.HttpCaptureVpnService
import com.fly.httpcapture.vpn.VpnState

class CaptureTileService : TileService() {
    private val mainHandler by lazy { Handler(Looper.getMainLooper()) }

    override fun onStartListening() {
        super.onStartListening()
        refresh()
    }

    @SuppressLint("StartActivityAndCollapseDeprecated")
    override fun onClick() {
        super.onClick()
        if (VpnState.running) {
            stopFromTile()
            return
        }
        startFromTile()
    }

    private fun startFromTile() {
        val store = ConfigStore(this)
        val settings = store.load()
        val ready = settings.activeProfileId != null && settings.selectedPackages.isNotEmpty() && VpnService.prepare(this) == null
        if (!ready) {
            openApp("快捷磁贴需要先在 App 内完成代理配置、应用选择和系统 VPN 授权")
            return
        }
        val profile = settings.profiles.firstOrNull { it.id == settings.activeProfileId }
        if (profile != null && ControlClient.canControl(profile)) {
            refresh(false, "启动中")
            Thread {
                runCatching {
                    val result = startOrAttachDesktopCapture(settings.activeCaptureId, profile, settings.selectedPackages)
                    val currentStore = ConfigStore(this)
                    currentStore.saveActiveCapture(result.captureId)
                    currentStore.saveVpnOnlyMode(false)
                    ContextCompat.startForegroundService(this, HttpCaptureVpnService.startIntent(this))
                    if (result.status == "starting") ControlClient.confirm(profile, result.captureId)
                }.onFailure {
                    refresh(false, "启动失败")
                    openApp("快捷磁贴启动失败：${it.message}。VPN 未启动")
                }.onSuccess {
                    refresh(true)
                }
            }.start()
            return
        }
        store.saveActiveCapture(null)
        store.saveVpnOnlyMode(true)
        ContextCompat.startForegroundService(this, HttpCaptureVpnService.startIntent(this))
        refresh(true)
    }

    private fun stopFromTile() {
        refresh(true, "停止中")
        Thread {
            val store = ConfigStore(this)
            val settings = store.load()
            val profile = settings.profiles.firstOrNull { it.id == settings.activeProfileId }
            val captureId = settings.activeCaptureId
            if (settings.vpnOnlyMode || captureId == null) {
                startService(HttpCaptureVpnService.stopIntent(this))
                store.saveVpnOnlyMode(false)
                refresh(false)
                return@Thread
            }
            if (profile == null || !ControlClient.canControl(profile)) {
                startService(HttpCaptureVpnService.stopIntent(this))
                store.saveActiveCapture(null)
                store.saveVpnOnlyMode(false)
                refresh(false)
                return@Thread
            }
            runCatching {
                val status = ControlClient.status(profile)
                startService(HttpCaptureVpnService.stopIntent(this))
                stopDesktopCaptureIfMatching(profile, captureId, status)
                store.saveActiveCapture(null)
                store.saveVpnOnlyMode(false)
            }.onFailure {
                startService(HttpCaptureVpnService.stopIntent(this))
                refresh(false, "状态未知")
                openApp("手机 VPN 已停止，但快捷磁贴无法确认电脑端状态：${it.message}")
            }.onSuccess {
                refresh(false)
            }
        }.start()
    }

    private fun startOrAttachDesktopCapture(
        activeCaptureId: String?,
        profile: CaptureProfile,
        packages: Set<String>,
    ): ControlCaptureResult {
        val status = ControlClient.status(profile)
        if (status.status == "idle") {
            ConfigStore(this).saveActiveCapture(null)
            return ControlClient.start(profile, packages, Build.MODEL)
        }
        val desktopCaptureId = status.captureId
        if (desktopCaptureId.isNullOrBlank()) {
            error("电脑端状态为 ${status.status}，但没有返回活动会话 ID")
        }
        if (activeCaptureId == null) {
            error("电脑端已有活动会话 $desktopCaptureId，请在 App 内确认后再继续")
        }
        if (activeCaptureId != desktopCaptureId) {
            error("电脑端活动会话是 $desktopCaptureId，本机记录的是 $activeCaptureId")
        }
        if (status.status != "starting" && status.status != "recording") {
            error("电脑端会话 $desktopCaptureId 当前为 ${status.status}，请在 App 内确认后再继续")
        }
        return ControlCaptureResult(desktopCaptureId, status.status, null)
    }

    private fun stopDesktopCaptureIfMatching(
        profile: CaptureProfile,
        captureId: String,
        status: ControlStatus,
    ) {
        if (status.status == "idle") return
        val desktopCaptureId = status.captureId
        if (desktopCaptureId != captureId) {
            error("电脑端活动会话是 ${desktopCaptureId ?: "未知"}，不是本机记录的 $captureId")
        }
        ControlClient.stop(profile, captureId)
    }

    @SuppressLint("StartActivityAndCollapseDeprecated")
    private fun openApp(message: String) {
        mainHandler.post {
            val intent = Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                .putExtra(EXTRA_MESSAGE, message)
            if (Build.VERSION.SDK_INT >= 34) {
                startActivityAndCollapse(
                    PendingIntent.getActivity(
                        this,
                        12,
                        intent,
                        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
                    )
                )
            } else {
                @Suppress("DEPRECATION") startActivityAndCollapse(intent)
            }
        }
    }

    private fun refresh(running: Boolean = VpnState.running, subtitle: String? = null) {
        if (Looper.myLooper() != Looper.getMainLooper()) {
            mainHandler.post { refresh(running, subtitle) }
            return
        }
        qsTile?.apply {
            state = if (running) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
            label = if (running) "停止抓包" else "开始抓包"
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                this.subtitle = subtitle.orEmpty()
            }
            updateTile()
        }
    }

    companion object {
        const val EXTRA_MESSAGE = "com.fly.httpcapture.tile.MESSAGE"
    }
}
