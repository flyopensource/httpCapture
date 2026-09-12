package com.fly.httpcapture.vpn

import android.content.Context
import android.content.Intent
import android.content.ComponentName
import android.service.quicksettings.TileService
import com.fly.httpcapture.tile.CaptureTileService

object VpnState {
    const val ACTION_STATE_CHANGED = "com.fly.httpcapture.VPN_STATE_CHANGED"
    const val EXTRA_RUNNING = "running"
    const val EXTRA_ERROR = "error"
    @Volatile var running: Boolean = false
        private set
    @Volatile var lastError: String? = null
        private set

    fun update(context: Context, isRunning: Boolean, error: String? = null) {
        running = isRunning
        lastError = error
        context.sendBroadcast(Intent(ACTION_STATE_CHANGED).setPackage(context.packageName).apply {
            putExtra(EXTRA_RUNNING, isRunning)
            putExtra(EXTRA_ERROR, error)
        })
        runCatching {
            TileService.requestListeningState(context, ComponentName(context, CaptureTileService::class.java))
        }
    }
}
