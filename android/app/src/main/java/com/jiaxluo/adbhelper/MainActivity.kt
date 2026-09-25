/*
 * 文件名称 : MainActivity.kt
 * 功    能 : 主界面 —— 单屏三卡：设备连接 / 安装 APK / 抓取日志。
 *            交互与已评审原型 docs/ui-mockup-android-1.0.0.html 对齐：
 *              M2 连接卡：输入/连接中/已连接、历史直连+删除、置灰联动、断线巡检
 *              M3 安装卡：SAF 选 APK → 安装 → 成功/失败(中文原因+重试)
 *              M4 日志卡：开始抓取(前台服务) → 计时/体积 → 结束并分享 / 断线自动停止
 *            连接保活：巡检 8s + 回前台立即 ensureConnection（静默重连，见 DeviceManager）
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M4（M2 初版 + M3 安装卡 + M4 日志卡）
 */
package com.jiaxluo.adbhelper

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.core.content.FileProvider
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

// 品牌色（与原型一致）
private val BrandBlue = Color(0xFF2563EB)
private val SuccessGreen = Color(0xFF16A34A)
private val DangerRed = Color(0xFFDC2626)
private val PageBg = Color(0xFFF4F6F9)
private val TextMain = Color(0xFF1F2937)
private val TextMuted = Color(0xFF6B7280)

class MainActivity : ComponentActivity() {

    private val deviceManager: DeviceManager by lazy { AppGraph.deviceManager }
    private val installManager: InstallManager by lazy { AppGraph.installManager }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 崩溃捕获已在 App(Application) 安装，此处不重复装（幂等在 CrashReporter 内也有护栏）
        setContent {
            MaterialTheme(colorScheme = MaterialTheme.colorScheme.copy(
                primary = BrandBlue, background = PageBg
            )) {
                MainScreen(deviceManager, installManager)
            }
        }
    }
}

@Composable
fun MainScreen(manager: DeviceManager, installManager: InstallManager) {
    val state by manager.state.collectAsState()
    var historyVersion by remember { mutableStateOf(0) }   // 历史增删后触发重组
    val scope = rememberCoroutineScope()
    var toast by remember { mutableStateOf<String?>(null) }
    val context = LocalContext.current

    // 连接巡检：每 8 秒 ensureConnection（掉线先静默重连，重连失败才提示+联动清理）；
    // 未连接且非手动断开时尝试自动连最近设备（覆盖进程被回收后 server 冷启场景）
    LaunchedEffect(state) {
        while (true) {
            delay(8000)
            val r = manager.ensureConnection(autoReconnect = true)
            if (r.dropped) {
                installManager.reset()
                // 抓取中断线：服务内 EOF 自会收尾，这里只复位 UI 态
            }
            r.message?.let { toast = it }
        }
    }

    // 回前台立即校验
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(lifecycleOwner) {
        lifecycleOwner.repeatOnLifecycle(Lifecycle.State.RESUMED) {
            val r = manager.ensureConnection(autoReconnect = true)
            if (r.dropped) {
                installManager.reset()
            }
            r.message?.let { toast = it }
        }
    }

    // SAF 选 APK
    val apkPicker = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri != null) {
            scope.launch {
                val err = installManager.pick(uri)
                if (err != null) {
                    toast = err
                }
            }
        }
    }

    val connected = state is DeviceState.Connected

    // 上次崩溃记录（有则顶部警告条：分享/忽略）
    var crashFile by remember { mutableStateOf(CrashReporter.latest(context)) }

    Box(modifier = Modifier.fillMaxSize().background(PageBg)) {
        Column(modifier = Modifier.fillMaxSize().padding(16.dp)) {
            // 标题栏
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("ADB 助手", fontSize = 20.sp, fontWeight = FontWeight.Bold, color = TextMain)
                Spacer(Modifier.width(8.dp))
                Text("V" + AppInfo.VERSION, fontSize = 11.sp, color = TextMuted,
                    modifier = Modifier
                        .background(Color.White, RoundedCornerShape(999.dp))
                        .padding(horizontal = 8.dp, vertical = 2.dp))
            }
            Spacer(Modifier.height(12.dp))

            crashFile?.let { cf ->
                CrashBanner(
                    onShare = {
                        shareFile(context, cf, "text/plain")
                        CrashReporter.clear(context)
                        crashFile = null
                    },
                    onDismiss = {
                        CrashReporter.clear(context)
                        crashFile = null
                    })
                Spacer(Modifier.height(10.dp))
            }

            DeviceCard(manager, state,
                onHistoryChanged = { historyVersion++ },
                onToast = { toast = it })
            Spacer(Modifier.height(10.dp))

            InstallCard(installManager, enabled = connected,
                onPickFile = {
                    // 部分文件管理器把 APK 标成 octet-stream，两个 mime 都给；
                    // 非法文件由 InstallManager 的 PK 魔数校验兜底
                    apkPicker.launch(arrayOf(
                        "application/vnd.android.package-archive",
                        "application/octet-stream"))
                },
                onToast = { toast = it })
            Spacer(Modifier.height(10.dp))

            LogCaptureCard(enabled = connected,
                serial = manager.currentSerial,
                onToast = { toast = it },
                onShare = { file ->
                    shareFile(context, file, "text/plain")
                })        }

        toast?.let { msg ->
            ToastBar(msg) { toast = null }
        }
    }
}

/** 崩溃记录警告条 */
@Composable
fun CrashBanner(onShare: () -> Unit, onDismiss: () -> Unit) {
    Column(modifier = Modifier
        .fillMaxWidth()
        .background(Color(0xFFFFF4E5), RoundedCornerShape(10.dp))
        .padding(horizontal = 12.dp, vertical = 9.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("⚠️", fontSize = 14.sp)
            Spacer(Modifier.width(8.dp))
            Text("上次异常退出已记录崩溃日志",
                fontSize = 12.5.sp, fontWeight = FontWeight.Medium, color = Color(0xFF92600A),
                modifier = Modifier.weight(1f))
        }
        Spacer(Modifier.height(6.dp))
        Row {
            Button(onClick = onShare, shape = RoundedCornerShape(8.dp),
                contentPadding = androidx.compose.foundation.layout.PaddingValues(
                    horizontal = 14.dp, vertical = 4.dp),
                modifier = Modifier.height(30.dp)) {
                Text("分享排查", fontSize = 12.sp)
            }
            Spacer(Modifier.width(8.dp))
            OutlinedButton(onClick = onDismiss, shape = RoundedCornerShape(8.dp),
                contentPadding = androidx.compose.foundation.layout.PaddingValues(
                    horizontal = 14.dp, vertical = 4.dp),
                modifier = Modifier.height(30.dp)) {
                Text("忽略", fontSize = 12.sp, color = TextMuted)
            }
        }
    }
}

/** FileProvider 分享任意文件 */
private fun shareFile(context: android.content.Context, file: java.io.File, mime: String) {
    val uri = FileProvider.getUriForFile(context,
        "com.jiaxluo.adbhelper.fileprovider", file)
    val send = Intent(Intent.ACTION_SEND).apply {
        type = mime
        putExtra(Intent.EXTRA_STREAM, uri)
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    context.startActivity(Intent.createChooser(send, "分享文件"))
}

/* ==================== 卡1：设备连接 ==================== */

@Composable
fun DeviceCard(
    manager: DeviceManager,
    state: DeviceState,
    onHistoryChanged: () -> Unit,
    onToast: (String) -> Unit
) {
    val scope = rememberCoroutineScope()
    var input by remember { mutableStateOf("") }

    Card(colors = CardDefaults.cardColors(containerColor = Color.White),
        shape = RoundedCornerShape(12.dp)) {
        Column(modifier = Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("🔗 设备连接", fontSize = 15.sp, fontWeight = FontWeight.SemiBold, color = TextMain)
                Spacer(Modifier.weight(1f))
                Text(
                    when (state) {
                        is DeviceState.Disconnected -> "Wi-Fi 直连电视/盒子"
                        is DeviceState.Connecting -> "连接中…"
                        is DeviceState.Connected -> "已连接"
                    },
                    fontSize = 12.sp, color = TextMuted
                )
            }
            Spacer(Modifier.height(10.dp))

            when (state) {
                is DeviceState.Disconnected -> {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        OutlinedTextField(
                            value = input,
                            onValueChange = { input = it },
                            modifier = Modifier.weight(1f),
                            singleLine = true,
                            placeholder = { Text("电视/盒子 IP（可带 :5555）", fontSize = 14.sp) },
                            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go),
                            keyboardActions = KeyboardActions(onGo = {
                                val v = input
                                input = ""
                                scope.launch {
                                    val r = manager.connect(v)
                                    onToast(r.message)
                                }
                            }),
                            shape = RoundedCornerShape(9.dp)
                        )
                        Spacer(Modifier.width(8.dp))
                        Button(onClick = {
                            val v = input
                            input = ""
                            scope.launch {
                                val r = manager.connect(v)
                                onToast(r.message)
                            }
                        }, shape = RoundedCornerShape(9.dp)) {
                            Text("连接")
                        }
                    }
                    val hist = manager.history.all
                    if (hist.isNotEmpty()) {
                        Spacer(Modifier.height(6.dp))
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            HorizontalDivider(modifier = Modifier.weight(1f), color = Color(0xFFE5E9F0))
                            Text(" 最近连接 · 点击直连 ", fontSize = 11.sp, color = TextMuted)
                            HorizontalDivider(modifier = Modifier.weight(1f), color = Color(0xFFE5E9F0))
                        }
                        LazyColumn {
                            items(hist, key = { it.serial }) { h ->
                                HistoryRow(h, onClick = {
                                    scope.launch {
                                        val r = manager.connectHistory(h)
                                        onToast(r.message)
                                        if (r.ok) {
                                            onHistoryChanged()
                                        }
                                    }
                                }, onDelete = {
                                    manager.history.remove(h.serial)
                                    onHistoryChanged()
                                })
                            }
                        }
                    }
                }

                is DeviceState.Connecting -> {
                    Row(verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.padding(vertical = 8.dp)) {
                        CircularProgressIndicator(modifier = Modifier.size(18.dp), strokeWidth = 2.5.dp)
                        Spacer(Modifier.width(10.dp))
                        Text("正在连接 ${state.serial} …", fontSize = 13.5.sp, color = TextMuted)
                    }
                }

                is DeviceState.Connected -> {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(10.dp).background(SuccessGreen, CircleShape))
                        Spacer(Modifier.width(10.dp))
                        Column(Modifier.weight(1f)) {
                            Text(state.serial, fontSize = 14.5.sp, fontWeight = FontWeight.Bold, color = TextMain)
                            Text("${state.brand} ${state.model} · Android ${state.android}",
                                fontSize = 12.sp, color = TextMuted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        OutlinedButton(onClick = {
                            scope.launch {
                                val r = manager.disconnect()
                                onToast(r.message)
                            }
                        }, shape = RoundedCornerShape(9.dp)) {
                            Text("断开", color = DangerRed)
                        }
                    }
                }
            }
        }
    }
}

@Composable
fun HistoryRow(entry: HistoryEntry, onClick: () -> Unit, onDelete: () -> Unit) {
    Row(modifier = Modifier
        .fillMaxWidth()
        .clickable { onClick() }
        .padding(vertical = 8.dp, horizontal = 4.dp),
        verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(8.dp).background(Color(0xFFC3CBD8), CircleShape))
        Spacer(Modifier.width(10.dp))
        Column(Modifier.weight(1f)) {
            Text(entry.serial, fontSize = 13.5.sp, fontWeight = FontWeight.SemiBold, color = TextMain)
            val time = SimpleDateFormat("MM-dd HH:mm", Locale.CHINA).format(Date(entry.lastConnected))
            Text("${entry.brand} ${entry.model} · $time",
                fontSize = 11.5.sp, color = TextMuted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Text("✕", fontSize = 15.sp, color = Color(0xFFB6BFCC),
            modifier = Modifier
                .clickable { onDelete() }
                .padding(6.dp))
    }
}

/* ==================== 卡2：安装 APK（M3） ==================== */

@Composable
fun InstallCard(
    manager: InstallManager,
    enabled: Boolean,
    onPickFile: () -> Unit,
    onToast: (String) -> Unit
) {
    val state by manager.state.collectAsState()
    val scope = rememberCoroutineScope()
    Card(colors = CardDefaults.cardColors(containerColor = Color.White),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.alpha(if (enabled) 1f else 0.45f)) {
        Column(modifier = Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("📦 安装 APK", fontSize = 15.sp, fontWeight = FontWeight.SemiBold, color = TextMain)
                Spacer(Modifier.weight(1f))
                Text(
                    when {
                        !enabled -> "请先连接设备"
                        else -> when (state) {
                            is InstallState.Idle -> "覆盖安装 · 保留应用数据"
                            is InstallState.Picked -> "已选文件"
                            is InstallState.Installing -> "安装中…"
                            is InstallState.Done -> "安装成功"
                            is InstallState.Fail -> "安装失败"
                        }
                    },
                    fontSize = 12.sp, color = TextMuted
                )
            }
            if (!enabled) {
                return@Column   // 置灰：不渲染内部控件，自然不可点
            }
            Spacer(Modifier.height(10.dp))

            when (val s = state) {
                is InstallState.Idle -> {
                    Button(onClick = onPickFile, shape = RoundedCornerShape(9.dp),
                        modifier = Modifier.fillMaxWidth()) {
                        Text("选择 APK 安装")
                    }
                }

                is InstallState.Picked -> {
                    FileChip(s.fileName, formatSize(s.sizeBytes))
                    Spacer(Modifier.height(8.dp))
                    Row {
                        Button(onClick = {
                            scope.launch {
                                val r = manager.install()
                                onToast(r.message)
                            }
                        }, shape = RoundedCornerShape(9.dp), modifier = Modifier.weight(1f)) {
                            Text("开始安装")
                        }
                        Spacer(Modifier.width(8.dp))
                        OutlinedButton(onClick = { manager.reset() },
                            shape = RoundedCornerShape(9.dp)) {
                            Text("换个文件", color = TextMuted)
                        }
                    }
                }

                is InstallState.Installing -> {
                    Row(verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.padding(vertical = 6.dp)) {
                        CircularProgressIndicator(modifier = Modifier.size(18.dp), strokeWidth = 2.5.dp)
                        Spacer(Modifier.width(10.dp))
                        Text("正在传输并安装 ${s.fileName} …（大包需等待）",
                            fontSize = 12.5.sp, color = TextMuted, maxLines = 2)
                    }
                }

                is InstallState.Done -> {
                    ResultRow(ok = true, main = "安装成功", sub = s.displayName)
                    Spacer(Modifier.height(8.dp))
                    Button(onClick = { manager.reset() }, shape = RoundedCornerShape(9.dp),
                        modifier = Modifier.fillMaxWidth()) {
                        Text("再装一个")
                    }
                }

                is InstallState.Fail -> {
                    ResultRow(ok = false, main = "安装失败：" + s.reason, sub = s.raw)
                    Spacer(Modifier.height(8.dp))
                    Row {
                        Button(onClick = {
                            scope.launch {
                                val r = manager.retry()
                                onToast(r.message)
                            }
                        }, shape = RoundedCornerShape(9.dp), modifier = Modifier.weight(1f)) {
                            Text("重试")
                        }
                        Spacer(Modifier.width(8.dp))
                        OutlinedButton(onClick = { manager.reset() },
                            shape = RoundedCornerShape(9.dp)) {
                            Text("换个文件", color = TextMuted)
                        }
                    }
                }

                else -> Unit
            }
        }
    }
}

/* ==================== 卡3：抓取日志（M4） ==================== */

@Composable
fun LogCaptureCard(
    enabled: Boolean,
    serial: String?,
    onToast: (String) -> Unit,
    onShare: (File) -> Unit
) {
    val context = LocalContext.current
    val capture by LogCaptureService.state.collectAsState()
    val liveBytes by LogCaptureService.bytes.collectAsState()
    val scope = rememberCoroutineScope()
    // 每秒跳动的时钟。key 必须是 capture 实例本身：
    // 「再抓一份」产生新 Running 实例时循环要重启（历史 bug：key 用 isRunning 布尔，
    // Running→Running 不重启 → nowMs 停在旧值 → 新抓取显示 00:-xx 负数、体积冻结）
    var nowMs by remember { mutableStateOf(System.currentTimeMillis()) }
    LaunchedEffect(capture) {
        if (capture is CaptureState.Running) {
            while (LogCaptureService.state.value === capture &&
                LogCaptureService.state.value is CaptureState.Running) {
                delay(250)
                nowMs = System.currentTimeMillis()
            }
        }
    }
    // 抓取中红点闪烁
    val blink = rememberInfiniteTransition(label = "blink")
    val dotAlpha by blink.animateFloat(
        initialValue = 1f, targetValue = 0.2f,
        animationSpec = infiniteRepeatable(tween(500), RepeatMode.Reverse),
        label = "dot"
    )

    Card(colors = CardDefaults.cardColors(containerColor = Color.White),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.alpha(if (enabled) 1f else 0.45f)) {
        Column(modifier = Modifier.padding(14.dp)) {
            // 有会话痕迹（Running/Done）时不置灰：设备掉线触发的自动收尾恰恰需要
            // 在断连态下取走文件，锁死分享入口等于丢日志（review #2）
            val hasSession = capture is CaptureState.Running || capture is CaptureState.Done
            val cardEnabled = enabled || hasSession
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("📄 抓取日志", fontSize = 15.sp, fontWeight = FontWeight.SemiBold, color = TextMain)
                Spacer(Modifier.weight(1f))
                Text(
                    when {
                        !cardEnabled -> "请先连接设备"
                        capture is CaptureState.Running -> "抓取中…"
                        capture is CaptureState.Done -> "抓取完成"
                        else -> "全量日志 · 存手机 · 可分享"
                    },
                    fontSize = 12.sp, color = TextMuted
                )
            }
            if (!cardEnabled) {
                return@Column
            }
            Spacer(Modifier.height(10.dp))

            when (val s = capture) {
                is CaptureState.Idle -> {
                    // Android 13+ 通知需要运行时授权（不授权服务照跑，但息屏时看不到计时/停止按钮）
                    val notifPermLauncher = rememberLauncherForActivityResult(
                        ActivityResultContracts.RequestPermission()
                    ) { granted ->
                        // <33 系统请求自动返回 false，不算用户拒绝、不提示
                        if (!granted && android.os.Build.VERSION.SDK_INT >= 33) {
                            onToast("未授权通知：抓取照常进行，但通知栏不显示状态")
                        }
                        // 无论授权与否都启动抓取
                        if (serial == null) {
                            onToast("设备未连接")
                            return@rememberLauncherForActivityResult
                        }
                        context.startForegroundService(
                            Intent(context, LogCaptureService::class.java).apply {
                                action = LogCaptureService.ACTION_START
                                putExtra(LogCaptureService.EXTRA_SERIAL, serial)
                            })
                    }
                    Button(onClick = {
                        if (serial == null) {
                            onToast("设备未连接")
                            return@Button
                        }
                        // 33+ 未授权则先弹授权框（回调里启动抓取）；已授权/低版本直接启动
                        notifPermLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
                    }, shape = RoundedCornerShape(9.dp), modifier = Modifier.fillMaxWidth()) {
                        Text("▶ 开始抓取")
                    }
                    Spacer(Modifier.height(6.dp))
                    Text("抓取全量 logcat 保存到手机，可退出 App / 息屏继续",
                        fontSize = 11.5.sp, color = TextMuted)
                }

                is CaptureState.Running -> {
                    // 读取 nowMs/liveBytes：每 250ms 触发本卡片重组，计时/体积随之刷新
                    nowMs
                    // 防负：nowMs 初值是组合时刻，startAt 是服务 set 时刻，跨实例可能倒挂
                    val sec = maxOf(0L, (nowMs - s.startedAt) / 1000)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(10.dp).alpha(dotAlpha).background(DangerRed, CircleShape))
                        Spacer(Modifier.width(10.dp))
                        Text(fmtSec(sec), fontSize = 22.sp, fontWeight = FontWeight.Bold, color = TextMain)
                        Spacer(Modifier.width(10.dp))
                        // 体积以服务端写入计数为准（StateFlow 驱动，不依赖本卡片重组）
                        Text(formatSize(liveBytes), fontSize = 12.5.sp, color = TextMuted)
                    }
                    Spacer(Modifier.height(8.dp))
                    Text("可退出 App / 息屏，后台继续抓取（通知栏可见）",
                        fontSize = 11.5.sp, color = Color(0xFF92600A),
                        modifier = Modifier
                            .fillMaxWidth()
                            .background(Color(0xFFFFF8E6), RoundedCornerShape(8.dp))
                            .padding(horizontal = 10.dp, vertical = 7.dp))
                    Spacer(Modifier.height(8.dp))
                    Button(onClick = {
                        context.startService(Intent(context, LogCaptureService::class.java)
                            .setAction(LogCaptureService.ACTION_STOP))
                        // 对齐原型：结束即拉起分享面板。等服务状态真正转 Done（文件已关闭）再分享，
                        // 上限等 3s 防服务异常时卡死按钮
                        val f = s.file
                        scope.launch {
                            var waited = 0
                            while (LogCaptureService.state.value is CaptureState.Running && waited < 3000) {
                                delay(100)
                                waited += 100
                            }
                            onShare(f)
                        }
                    }, colors = ButtonDefaults.buttonColors(containerColor = DangerRed),
                        shape = RoundedCornerShape(9.dp), modifier = Modifier.fillMaxWidth()) {
                        Text("■ 结束并分享")
                    }
                }

                is CaptureState.Done -> {
                    // 点文件卡显示/收起完整保存路径
                    var showPath by remember { mutableStateOf(false) }
                    Column(modifier = Modifier.clickable { showPath = !showPath }) {
                        FileChip(s.file.name, formatSize(s.bytes) + " · 抓取 " + fmtSec(s.durationSec))
                        if (showPath) {
                            Spacer(Modifier.height(4.dp))
                            Text("保存位置：${s.file.absolutePath}",
                                fontSize = 11.sp, color = TextMuted)
                            Text("（App 私有目录，点「分享」即可发出，无需手动找文件）",
                                fontSize = 10.5.sp, color = TextMuted)
                        }
                    }
                    Spacer(Modifier.height(8.dp))
                    Row {
                        Button(onClick = { onShare(s.file) },
                            shape = RoundedCornerShape(9.dp), modifier = Modifier.weight(1f)) {
                            Text("分享")
                        }
                        Spacer(Modifier.width(8.dp))
                        OutlinedButton(onClick = {
                            LogCaptureService.reset()
                        }, shape = RoundedCornerShape(9.dp)) {
                            Text("再抓一份", color = TextMuted)
                        }
                    }
                    if (s.autoStopped) {
                        Spacer(Modifier.height(6.dp))
                        Text("设备断开，已自动停止并保存",
                            fontSize = 11.5.sp, color = Color(0xFF92600A))
                    }
                }
            }
        }
    }
}

/* ==================== 公共小组件 ==================== */

@Composable
fun FileChip(name: String, size: String) {
    Row(verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .background(Color(0xFFF7F9FC), RoundedCornerShape(10.dp))
            .padding(horizontal = 11.dp, vertical = 9.dp)) {
        Text("📄", fontSize = 18.sp)
        Spacer(Modifier.width(9.dp))
        Column {
            Text(name, fontSize = 13.sp, fontWeight = FontWeight.SemiBold, color = TextMain,
                maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text(size, fontSize = 11.5.sp, color = TextMuted)
        }
    }
}

@Composable
fun ResultRow(ok: Boolean, main: String, sub: String) {
    val bg = if (ok) Color(0xFFEAFAF0) else Color(0xFFFDEEEE)
    val fg = if (ok) Color(0xFF14532D) else Color(0xFF7F1D1D)
    Column(modifier = Modifier
        .fillMaxWidth()
        .background(bg, RoundedCornerShape(10.dp))
        .padding(horizontal = 12.dp, vertical = 10.dp)) {
        Row {
            Text(if (ok) "✓" else "✗", fontWeight = FontWeight.Bold, color = fg, fontSize = 14.sp)
            Spacer(Modifier.width(8.dp))
            Text(main, color = fg, fontSize = 13.sp, fontWeight = FontWeight.Medium)
        }
        if (sub.isNotBlank()) {
            Spacer(Modifier.height(2.dp))
            Text(sub, color = TextMuted, fontSize = 11.5.sp,
                maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
    }
}

@Composable
fun ToastBar(message: String, onDismiss: () -> Unit) {
    LaunchedEffect(message) {
        delay(2200)
        onDismiss()
    }
    Box(modifier = Modifier.fillMaxSize().padding(bottom = 60.dp),
        contentAlignment = Alignment.BottomCenter) {
        Text(message,
            color = Color.White,
            fontSize = 12.5.sp,
            modifier = Modifier
                .background(Color(0xF011181F), RoundedCornerShape(999.dp))
                .padding(horizontal = 18.dp, vertical = 8.dp))
    }
}

/* ==================== 工具 ==================== */

private fun formatSize(bytes: Long): String = when {
    bytes >= 1024 * 1024 -> "%.1f MB".format(bytes / 1048576.0)
    bytes >= 1024 -> "%.0f KB".format(bytes / 1024.0)
    else -> "$bytes B"
}

private fun fmtSec(s: Long): String {
    val m = s / 60
    val r = s % 60
    return "%02d:%02d".format(m, r)
}
