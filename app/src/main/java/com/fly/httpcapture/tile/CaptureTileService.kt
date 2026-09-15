package com.fly.httpcapture.tile

import android.app.PendingIntent
import android.annotation.SuppressLint
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import androidx.core.content.ContextCompat
import com.fly.httpcapture.MainActivity
import com.fly.httpcapture.config.ConfigStore
import com.fly.httpcapture.config.ControlClient
import com.fly.httpcapture.vpn.HttpCaptureVpnService
import com.fly.httpcapture.vpn.VpnState

class CaptureTileService : TileService() {
    override fun onStartListening() {
        super.onStartListening()
        refresh()
    }

    @SuppressLint("StartActivityAndCollapseDeprecated")
    override fun onClick() {
        super.onClick()
        if (VpnState.running) {
            val store = ConfigStore(this)
            val settings = store.load()
            val profile = settings.profiles.firstOrNull { it.id == settings.activeProfileId }
            val captureId = settings.activeCaptureId
            startService(HttpCaptureVpnService.stopIntent(this))
            if (settings.vpnOnlyMode || captureId == null) {
                store.saveVpnOnlyMode(false)
            } else if (profile != null && ControlClient.canControl(profile)) {
                Thread {
                    runCatching { ControlClient.stop(profile, captureId) }
                        .onSuccess {
                            val currentStore = ConfigStore(this)
                            currentStore.saveActiveCapture(null)
                            currentStore.saveVpnOnlyMode(false)
                        }
                }.start()
            }
            refresh(false)
            return
        }
        val store = ConfigStore(this)
        val settings = store.load()
        val ready = settings.activeProfileId != null && settings.selectedPackages.isNotEmpty() && VpnService.prepare(this) == null
        if (!ready) {
            val intent = Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (Build.VERSION.SDK_INT >= 34) {
                startActivityAndCollapse(PendingIntent.getActivity(this, 12, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            } else {
                @Suppress("DEPRECATION") startActivityAndCollapse(intent)
            }
            return
        }
        val profile = settings.profiles.firstOrNull { it.id == settings.activeProfileId }
        if (profile != null && ControlClient.canControl(profile)) {
            Thread {
                runCatching {
                    val result = ControlClient.start(profile, settings.selectedPackages, Build.MODEL)
                    val currentStore = ConfigStore(this)
                    currentStore.saveActiveCapture(result.captureId)
                    currentStore.saveVpnOnlyMode(false)
                    ContextCompat.startForegroundService(this, HttpCaptureVpnService.startIntent(this))
                    ControlClient.confirm(profile, result.captureId)
                }
            }.start()
            refresh(true)
            return
        }
        store.saveActiveCapture(null)
        store.saveVpnOnlyMode(true)
        ContextCompat.startForegroundService(this, HttpCaptureVpnService.startIntent(this))
        refresh(true)
    }

    private fun refresh(running: Boolean = VpnState.running) {
        qsTile?.apply {
            state = if (running) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
            label = if (running) "停止抓包" else "开始抓包"
            updateTile()
        }
    }
}
