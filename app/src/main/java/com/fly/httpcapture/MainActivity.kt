package com.fly.httpcapture

import android.Manifest
import android.app.StatusBarManager
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.graphics.drawable.Icon
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.widget.Toast
import android.widget.ImageView
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.fly.httpcapture.config.CaptureProfile
import com.fly.httpcapture.config.CaptureSettings
import com.fly.httpcapture.config.CertificateUtils
import com.fly.httpcapture.config.ConfigStore
import com.fly.httpcapture.config.PairingCodec
import com.fly.httpcapture.config.PairingClient
import com.fly.httpcapture.config.ProfileAddress
import com.fly.httpcapture.config.ProfileAddressPolicy
import com.fly.httpcapture.tile.CaptureTileService
import com.fly.httpcapture.vpn.HttpCaptureVpnService
import com.fly.httpcapture.vpn.VpnState
import com.google.zxing.BinaryBitmap
import com.google.zxing.MultiFormatReader
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.common.HybridBinarizer
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    private val store by lazy { ConfigStore(this) }
    private var settingsState by mutableStateOf(CaptureSettings())
    private var messageState by mutableStateOf<String?>(null)
    private var runningState by mutableStateOf(false)
    private var pairingImportRunning = false

    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (it.resultCode == RESULT_OK) startVpn() else messageState = "未获得系统 VPN 授权"
    }
    private val certificateInstaller = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        reload()
        messageState = "已返回应用；正在重新检查系统证书"
    }
    private val scanner = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let(::importPairing)
    }

    private val stateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            runningState = intent?.getBooleanExtra(VpnState.EXTRA_RUNNING, false) ?: false
            intent?.getStringExtra(VpnState.EXTRA_ERROR)?.let { messageState = it }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        ContextCompat.registerReceiver(
            this, stateReceiver, IntentFilter(VpnState.ACTION_STATE_CHANGED), ContextCompat.RECEIVER_NOT_EXPORTED
        )
        runningState = VpnState.running
        reload()
        handleIntent(intent)
        setContent { HttpCaptureScreen() }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleIntent(intent)
    }

    override fun onResume() {
        super.onResume()
        reload()
        runningState = VpnState.running
    }

    override fun onDestroy() {
        unregisterReceiver(stateReceiver)
        super.onDestroy()
    }

    private fun reload() {
        settingsState = store.load()
    }

    private fun handleIntent(intent: Intent?) {
        if (intent?.action == Intent.ACTION_VIEW) intent.dataString?.let(::importPairing)
    }

    private fun importPairing(raw: String) {
        if (pairingImportRunning) {
            messageState = "正在导入代理配置，请稍候"
            return
        }
        pairingImportRunning = true
        lifecycleScope.launch {
            runCatching {
                val reference = PairingCodec.decodeReference(raw)
                messageState = "正在从电脑获取代理配置…"
                val profile = withContext(Dispatchers.IO) { PairingClient.download(reference) }
                CertificateUtils.validate(profile)
                store.upsertProfile(profile)
                reload()
                val ackError = withContext(Dispatchers.IO) {
                    runCatching { PairingClient.acknowledge(reference) }.exceptionOrNull()
                }
                val installState = if (CertificateUtils.isInstalled(this@MainActivity, profile)) {
                    "CA 已安装"
                } else {
                    "请继续安装 CA 证书"
                }
                messageState = if (ackError == null) {
                    "已导入 ${profile.name}；$installState"
                } else {
                    "已导入 ${profile.name}；$installState。电脑端确认失败，可手动关闭 CLI"
                }
            }.onFailure {
                messageState = "导入失败：${it.message}。请确认手机和电脑在同一局域网，且 CLI 仍在运行"
            }
            pairingImportRunning = false
        }
    }

    private fun requestStart() {
        val settings = store.load()
        if (settings.activeProfileId == null) {
            messageState = "请先扫码导入代理配置"
            return
        }
        if (settings.selectedPackages.isEmpty()) {
            messageState = "请先选择至少一个应用"
            return
        }
        val prepare = VpnService.prepare(this)
        if (prepare != null) vpnPermission.launch(prepare) else startVpn()
    }

    private fun startVpn() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 99)
        }
        ContextCompat.startForegroundService(this, HttpCaptureVpnService.startIntent(this))
    }

    private fun stopVpn() {
        startService(HttpCaptureVpnService.stopIntent(this))
    }

    private fun installCertificate(profile: CaptureProfile) {
        runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                val fileName = CertificateUtils.exportToDownloads(this, profile)
                messageState = "${profile.name} 的 CA 已保存到 Downloads/HTTP Capture/$fileName。请在安全设置中选择“安装 CA 证书”，再选择该文件。"
                Toast.makeText(this, "${profile.name} 的 CA 已保存。请选择“安装 CA 证书”并打开 $fileName", Toast.LENGTH_LONG).show()
                startActivity(Intent(Settings.ACTION_SECURITY_SETTINGS))
            } else {
                certificateInstaller.launch(CertificateUtils.installIntent(profile))
            }
        }
            .onFailure { messageState = "无法打开系统证书安装器：${it.message}" }
    }

    private fun scanFromCamera() {
        scanner.launch(
            ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                .setPrompt("扫描 httpcapture CLI 生成的二维码")
                .setBeepEnabled(false)
                .setCaptureActivity(PortraitCaptureActivity::class.java)
                .setOrientationLocked(true)
        )
    }

    private fun scanBitmap(uri: Uri) {
        runCatching {
            val bitmap = contentResolver.openInputStream(uri).use { BitmapFactory.decodeStream(it) }
                ?: error("无法读取图片")
            val pixels = IntArray(bitmap.width * bitmap.height)
            bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
            val source = RGBLuminanceSource(bitmap.width, bitmap.height, pixels)
            MultiFormatReader().decode(BinaryBitmap(HybridBinarizer(source))).text
        }.onSuccess(::importPairing).onFailure { messageState = "未识别到有效二维码：${it.message}" }
    }

    @Composable
    private fun HttpCaptureScreen() {
        var showApps by remember { mutableStateOf(false) }
        var editingProfile by remember { mutableStateOf<CaptureProfile?>(null) }
        val gallery = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { uri ->
            uri?.let(::scanBitmap)
        }
        val active = settingsState.profiles.firstOrNull { it.id == settingsState.activeProfileId }
        MaterialTheme {
            Surface(modifier = Modifier.fillMaxSize(), color = Color(0xFFF8FAFC)) {
                LazyColumn(
                    modifier = Modifier.fillMaxSize().padding(horizontal = 18.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    item {
                        Spacer(Modifier.height(12.dp))
                        Text("HTTP Capture", style = MaterialTheme.typography.headlineMedium)
                        Text("把指定应用的流量一键转发到电脑代理", color = Color(0xFF475569))
                    }
                    item {
                        Card(Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(9.dp)) {
                                Text("1. 代理配置", style = MaterialTheme.typography.titleMedium)
                                if (active == null) {
                                    Text("尚未导入配置", color = Color(0xFFB45309))
                                } else {
                                    Text("${active.name}  ·  ${active.engine.displayName}")
                                    Text("地址：${active.host}:${active.port}", style = MaterialTheme.typography.bodySmall)
                                    Text(
                                        "CA SHA-256：${active.certificateSha256.chunked(2).joinToString(":")}",
                                        style = MaterialTheme.typography.bodySmall,
                                    )
                                    val installed = remember(active, settingsState) { CertificateUtils.isInstalled(this@MainActivity, active) }
                                    val validityProblem = remember(active) { CertificateUtils.validityProblem(active) }
                                    Text(
                                        if (installed) "CA 已安装，可供接入 SDK 的 Debug 应用信任" else "CA 未安装：HTTP 可抓，HTTPS 尚不可解密",
                                        color = if (installed) Color(0xFF15803D) else Color(0xFFB45309),
                                    )
                                    if (validityProblem != null) {
                                        Text(validityProblem, color = Color(0xFFB91C1C))
                                    }
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        if (!installed) Button(onClick = { installCertificate(active) }) { Text("安装 CA") }
                                        OutlinedButton(onClick = {
                                            if (runningState) {
                                                messageState = "请先停止抓包，再修改代理 IP 和端口"
                                            } else {
                                                editingProfile = active
                                            }
                                        }) { Text("修改地址") }
                                        TextButton(onClick = { store.removeProfile(active.id); reload() }) { Text("删除") }
                                    }
                                }
                                if (settingsState.profiles.size > 1) {
                                    Text("已保存的代理配置")
                                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                        settingsState.profiles.take(3).forEach { profile ->
                                            FilterChip(
                                                selected = profile.id == active?.id,
                                                onClick = { store.selectProfile(profile.id); reload() },
                                                label = { Text(profile.name.take(12)) },
                                            )
                                        }
                                    }
                                }
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    Button(onClick = ::scanFromCamera) { Text("扫码导入") }
                                    OutlinedButton(onClick = { gallery.launch("image/*") }) { Text("从相册") }
                                }
                            }
                        }
                    }
                    item {
                        Card(Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Text("2. 选择应用", style = MaterialTheme.typography.titleMedium)
                                Text(
                                    if (settingsState.selectedPackages.isEmpty()) "尚未选择" else "已选择 ${settingsState.selectedPackages.size} 个应用",
                                    color = if (settingsState.selectedPackages.isEmpty()) Color(0xFFB45309) else Color(0xFF15803D),
                                )
                                settingsState.selectedPackages.take(4).forEach { Text(it, style = MaterialTheme.typography.bodySmall) }
                                OutlinedButton(onClick = { showApps = true }) { Text("选择一个或多个应用") }
                            }
                        }
                    }
                    item {
                        Button(
                            onClick = { if (runningState) stopVpn() else requestStart() },
                            modifier = Modifier.fillMaxWidth().height(54.dp),
                        ) { Text(if (runningState) "停止抓包" else "开始抓包") }
                        Text(
                            if (runningState) "VPN 已连接，仅转发所选应用" else "VPN 未连接",
                            color = if (runningState) Color(0xFF15803D) else Color(0xFF64748B),
                        )
                    }
                    item {
                        OutlinedButton(onClick = ::requestTile, modifier = Modifier.fillMaxWidth()) {
                            Text(if (Build.VERSION.SDK_INT >= 33) "添加快捷磁贴" else "在系统快捷设置中添加“抓包开关”")
                        }
                        Text("快捷磁贴会复用当前代理配置和应用选择。未配置或未授权时会打开本页。", style = MaterialTheme.typography.bodySmall)
                    }
                    messageState?.let { message ->
                        item {
                            Card(Modifier.fillMaxWidth()) { Text(message, Modifier.padding(12.dp)) }
                        }
                    }
                    item {
                        Text("边界：本 App 不保存请求内容、不做 MITM；未接 SDK 的第三方应用仅保证可转发的 HTTP。", style = MaterialTheme.typography.bodySmall)
                        Spacer(Modifier.height(20.dp))
                    }
                }
            }
        }
        if (showApps) {
            AppPickerDialog(
                initial = settingsState.selectedPackages,
                onDismiss = { showApps = false },
                onSave = { store.savePackages(it); reload(); showApps = false },
            )
        }
        editingProfile?.let { profile ->
            EditProfileAddressDialog(
                profile = profile,
                onDismiss = { editingProfile = null },
                onSave = { address ->
                    runCatching { store.updateProfileAddress(profile.id, address) }
                        .onSuccess {
                            reload()
                            editingProfile = null
                            messageState = "已更新 ${profile.name}：${address.host}:${address.port}"
                        }
                        .onFailure { messageState = "修改失败：${it.message}" }
                },
            )
        }
    }

    private fun requestTile() {
        if (Build.VERSION.SDK_INT >= 33) {
            getSystemService(StatusBarManager::class.java).requestAddTileService(
                ComponentName(this, CaptureTileService::class.java),
                getString(R.string.tile_name), Icon.createWithResource(this, R.drawable.ic_capture), mainExecutor,
            ) { result -> messageState = if (result == StatusBarManager.TILE_ADD_REQUEST_RESULT_TILE_ADDED) "快捷磁贴已添加" else "系统未添加快捷磁贴（结果 $result）" }
        } else {
            startActivity(Intent(Settings.ACTION_SETTINGS))
            messageState = "请下拉快捷设置并编辑磁贴，添加“抓包开关”"
        }
    }

    @Composable
    private fun AppPickerDialog(initial: Set<String>, onDismiss: () -> Unit, onSave: (Set<String>) -> Unit) {
        var selected by remember { mutableStateOf(initial) }
        var query by remember { mutableStateOf("") }
        var loadAttempt by remember { mutableIntStateOf(0) }
        var loadState by remember { mutableStateOf<AppLoadState>(AppLoadState.Loading) }
        LaunchedEffect(loadAttempt) {
            loadState = AppLoadState.Loading
            loadState = try {
                AppLoadState.Loaded(withContext(Dispatchers.IO) { loadLaunchableApps() })
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                AppLoadState.Failed(error.message ?: "未知错误")
            }
        }
        val apps = (loadState as? AppLoadState.Loaded)?.apps.orEmpty()
        val filtered = remember(apps, query) {
            apps.filter { query.isBlank() || it.label.contains(query, true) || it.packageName.contains(query, true) }
        }
        LaunchedEffect(loadState) {
            val loaded = loadState as? AppLoadState.Loaded ?: return@LaunchedEffect
            selected = selected.intersect(loaded.apps.mapTo(mutableSetOf(), AppRow::packageName))
        }
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text("选择抓包应用") },
            text = {
                Column {
                    OutlinedTextField(
                        query,
                        { query = it },
                        label = { Text("搜索名称或包名") },
                        enabled = loadState is AppLoadState.Loaded,
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Box(Modifier.fillMaxWidth().height(430.dp), contentAlignment = Alignment.Center) {
                        when (val state = loadState) {
                            AppLoadState.Loading -> Column(horizontalAlignment = Alignment.CenterHorizontally) {
                                CircularProgressIndicator()
                                Spacer(Modifier.height(12.dp))
                                Text("正在加载已安装应用…")
                            }
                            is AppLoadState.Failed -> Column(horizontalAlignment = Alignment.CenterHorizontally) {
                                Text("加载失败：${state.message}", color = Color(0xFFB91C1C))
                                Spacer(Modifier.height(8.dp))
                                OutlinedButton(onClick = { loadAttempt++ }) { Text("重试") }
                            }
                            is AppLoadState.Loaded -> if (filtered.isEmpty()) {
                                Text(if (query.isBlank()) "没有可选择的应用" else "没有匹配的应用")
                            } else {
                                LazyColumn(Modifier.fillMaxSize()) {
                                    items(filtered, key = { it.packageName }) { app ->
                                        val icon by produceState<android.graphics.drawable.Drawable?>(
                                            initialValue = null,
                                            key1 = app.packageName,
                                        ) {
                                            value = withContext(Dispatchers.IO) {
                                                runCatching { packageManager.getApplicationIcon(app.packageName) }.getOrNull()
                                            }
                                        }
                                        Row(
                                            Modifier.fillMaxWidth().padding(vertical = 7.dp),
                                            verticalAlignment = Alignment.CenterVertically,
                                        ) {
                                            AndroidView(
                                                factory = { context ->
                                                    ImageView(context).apply {
                                                        setImageResource(android.R.drawable.sym_def_app_icon)
                                                    }
                                                },
                                                update = { view ->
                                                    view.setImageDrawable(icon)
                                                    if (icon == null) view.setImageResource(android.R.drawable.sym_def_app_icon)
                                                },
                                                modifier = Modifier.size(38.dp),
                                            )
                                            Column(Modifier.weight(1f).padding(horizontal = 9.dp)) {
                                                Text(app.label)
                                                Text(app.packageName, style = MaterialTheme.typography.bodySmall)
                                                if (app.debugTrust) Text("HTTPS Debug Trust 已接入", color = Color(0xFF15803D), style = MaterialTheme.typography.labelSmall)
                                            }
                                            Checkbox(checked = app.packageName in selected, onCheckedChange = { checked ->
                                                selected = if (checked) selected + app.packageName else selected - app.packageName
                                            })
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            },
            confirmButton = {
                Button(onClick = { onSave(selected) }, enabled = loadState is AppLoadState.Loaded) {
                    Text("保存 (${selected.size})")
                }
            },
            dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
        )
    }

    @Composable
    private fun EditProfileAddressDialog(
        profile: CaptureProfile,
        onDismiss: () -> Unit,
        onSave: (ProfileAddress) -> Unit,
    ) {
        var host by remember(profile.id) { mutableStateOf(profile.host) }
        var port by remember(profile.id) { mutableStateOf(profile.port.toString()) }
        val validation = remember(host, port) { runCatching { ProfileAddressPolicy.parse(host, port) } }
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text("修改代理地址") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(profile.name, style = MaterialTheme.typography.titleSmall)
                    OutlinedTextField(
                        value = host,
                        onValueChange = { host = it },
                        label = { Text("IP 或 Host") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    OutlinedTextField(
                        value = port,
                        onValueChange = { port = it.filter(Char::isDigit).take(5) },
                        label = { Text("代理端口") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    validation.exceptionOrNull()?.message?.let {
                        Text(it, color = Color(0xFFB91C1C), style = MaterialTheme.typography.bodySmall)
                    }
                    Text("修改地址不会更换或重新安装 CA 证书。", style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = {
                Button(onClick = { onSave(validation.getOrThrow()) }, enabled = validation.isSuccess) {
                    Text("保存")
                }
            },
            dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
        )
    }

    private fun loadLaunchableApps(): List<AppRow> {
        return packageManager.getInstalledApplications(PackageManager.GET_META_DATA)
            .mapNotNull { info ->
                if (info.packageName == packageName || !info.enabled) return@mapNotNull null
                AppRow(
                    packageName = info.packageName,
                    label = info.loadLabel(packageManager).toString(),
                    debugTrust = info.metaData?.getBoolean("com.fly.httpcapture.DEBUG_TRUST", false) == true,
                )
            }
            .sortedBy { it.label.lowercase() }
    }

    private data class AppRow(
        val packageName: String,
        val label: String,
        val debugTrust: Boolean,
    )

    private sealed interface AppLoadState {
        data object Loading : AppLoadState
        data class Loaded(val apps: List<AppRow>) : AppLoadState
        data class Failed(val message: String) : AppLoadState
    }
}
