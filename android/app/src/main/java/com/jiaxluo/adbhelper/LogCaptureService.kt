/*
 * 文件名称 : LogCaptureService.kt
 * 功    能 : 日志抓取前台服务（M4 核心）—— 息屏/退后台不中断。
 *            长驻子进程 `adb -s <serial> logcat -v time`，stdout 逐块写入
 *            filesDir/logs/<ip>/adb_log_<yyyyMMdd_HHmmss>.log（命名与桌面版一致）。
 *            通知栏常驻：mm:ss 计时 + 已抓大小；结束后点通知可回 App。
 *            断线保护：UI 巡检发现掉线 → stopWithResult 自动结束保存已抓内容。
 *            logcat 退出（设备拔线时子进程自然 EOF）同样触发收尾。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M4 初版
 */
package com.jiaxluo.adbhelper

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Environment
import android.os.IBinder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** 抓取状态（UI 三态：Idle 就绪 / Running 抓取中 / Done 完成待分享） */
sealed class CaptureState {
    data object Idle : CaptureState()

    data class Running(
        val serial: String,
        val startedAt: Long,
        val file: File
    ) : CaptureState()

    data class Done(
        val file: File,
        val bytes: Long,
        val durationSec: Long,
        val serial: String,
        val autoStopped: Boolean     // true=设备断开自动停止（提示语不同）
    ) : CaptureState()
}

class LogCaptureService : Service() {

    companion object {
        const val CHANNEL_ID = "log_capture"
        const val NOTIFICATION_ID = 1001

        const val ACTION_START = "com.jiaxluo.adbhelper.CAPTURE_START"
        const val ACTION_STOP = "com.jiaxluo.adbhelper.CAPTURE_STOP"
        const val EXTRA_SERIAL = "serial"

        /** 结束后跳转标记：通知栏点回 App */
        const val EXTRA_FROM_NOTIF = "from_notif"

        private val _state = MutableStateFlow<CaptureState>(CaptureState.Idle)
        /** UI 订阅的抓取状态（static：进程内单例服务，生命周期与进程一致） */
        val state: StateFlow<CaptureState> = _state

        /** 服务端实时字节计数（UI 订阅显示体积——单一数据源，不依赖 UI 重组读文件） */
        private val _bytes = MutableStateFlow(0L)
        val bytes: StateFlow<Long> = _bytes

        val running: Boolean get() = _state.value is CaptureState.Running

        /** Done → Idle（「再抓一份」） */
        fun reset() {
            if (_state.value is CaptureState.Done) {
                _state.value = CaptureState.Idle
            }
        }

        /** 通知栏实时摘要（计时/体积），由写入循环每秒刷新 */
        @Volatile
        var notifText: String = ""
            private set
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var proc: Process? = null
    private var notifBuilder: android.app.Notification.Builder? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopCapture(userRequested = true)
                return START_NOT_STICKY
            }

            ACTION_START -> {
                val serial = intent.getStringExtra(EXTRA_SERIAL)
                if (serial != null && _state.value is CaptureState.Idle) {
                    startCapture(serial)
                }
            }
        }
        return START_NOT_STICKY
    }

    /* ==================== 抓取主流程 ==================== */

    private fun startCapture(serial: String) {
        // 日志目录：files/logs/<ip>/（FileProvider 已授权该路径）
        val dir = File(filesDir, "logs/" + serial.replace(":", "_")).apply { mkdirs() }
        val stamp = SimpleDateFormat("yyyyMMdd_HHmmss", Locale.CHINA).format(Date())
        val file = File(dir, "adb_log_$stamp.log")

        // 前台服务先行（5s 内必须 startForeground，否则 ANR 崩溃）
        val notification = buildNotification("准备抓取 $serial …")
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }

        _state.value = CaptureState.Running(serial, System.currentTimeMillis(), file)

        scope.launch {
            try {
                // 长驻 logcat：不设超时（waitFor 不调用），靠 destroy 收尾
                val pb = ProcessBuilder(AdbCore.binaryPath(this@LogCaptureService),
                    "-s", serial, "logcat", "-v", "time").apply {
                    redirectErrorStream(true)
                    val env = environment()
                    env["HOME"] = AdbCore.homeDir(this@LogCaptureService).absolutePath
                    env["TMPDIR"] = cacheDir.absolutePath
                    env["ANDROID_ADB_LOG_PATH"] =
                        File(AdbCore.homeDir(this@LogCaptureService), "server.log").absolutePath
                    env["LD_LIBRARY_PATH"] = applicationInfo.nativeLibraryDir
                }
                val started = System.currentTimeMillis()
                val p = pb.start()
                proc = p

                // 看门狗：每 3 秒确认设备仍在 devices 列表。作用有二：
                // a) adb server 被系统冻结/杀死导致管道停流时，devices 查询会失败/超时，
                //    据此把会话收尾成 Done(autoStopped) 而不是无限假跑
                // b) 设备真掉线时主动收尾，不依赖 EOF（有些 ROM 上 adb 不立即 EOF）
                // 容抖动（review #4）：单次查不到不算死——先静默重连一次（Wi-Fi 漫游/adb
                // 瞬时 offline 几秒自愈），连续 2 轮仍死才收尾，避免误杀长抓取
                val watchdog = launch {
                    var failStreak = 0
                    while (_state.value is CaptureState.Running) {
                        delay(3000)
                        if (_state.value !is CaptureState.Running) {
                            break
                        }
                        val devs = AdbCore.run(this@LogCaptureService, 8, "devices")
                        var alive = devs.ok &&
                            AdbParser.parseDevices(devs.output).any {
                                it.serial == serial && it.online
                            }
                        if (!alive) {
                            failStreak++
                            if (failStreak == 1) {
                                // 第一次：静默重连补救（已授权密钥，无弹框）
                                val r = AdbCore.run(this@LogCaptureService, 10, "connect", serial)
                                alive = r.output.contains("connected to")
                            }
                        } else {
                            failStreak = 0
                        }
                        if (!alive && failStreak >= 2) {
                            stopCapture(userRequested = false)
                            break
                        }
                    }
                }

                var bytes = 0L
                val buf = ByteArray(64 * 1024)
                var lastNotif = 0L
                _bytes.value = 0L
                // 抓取开始标记写进文件（与桌面版格式对齐：设备标识+起止时间）
                val header = "设备: $serial\n抓取开始: ${SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.CHINA).format(Date(started))}\n\n"
                file.outputStream().use { out ->
                    out.write(header.toByteArray())
                    bytes += header.toByteArray().size
                    _bytes.value = bytes
                    val input = p.inputStream
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) {
                            break       // 设备拔线/进程死亡 → EOF 自然结束
                        }
                        if (n > 0) {
                            out.write(buf, 0, n)
                            bytes += n
                            _bytes.value = bytes
                        }
                        // 通知栏每秒刷新计时/体积（息屏时用户靠它确认还在抓）
                        val now = System.currentTimeMillis()
                        if (now - lastNotif >= 1000) {
                            lastNotif = now
                            val sec = (now - started) / 1000
                            updateNotification(String.format(Locale.CHINA, "%02d:%02d · %s",
                                sec / 60, sec % 60, formatSize(bytes)))
                        }
                        if (_state.value !is CaptureState.Running) {
                            break       // 收到停止指令，尽快退出读循环
                        }
                    }
                }

                // 子进程收尾。正常路径也可能是 watchdog 已转 Done（管道 EOF 晚于判死）——不覆盖
                destroyProc()
                watchdog.cancel()
                val cur = _state.value as? CaptureState.Running
                if (cur != null) {
                    // EOF 收尾：区分设备真掉线与正常结束（review #11）——补一次存活探测
                    val devs = AdbCore.run(this@LogCaptureService, 8, "devices")
                    val alive = devs.ok &&
                        AdbParser.parseDevices(devs.output).any { it.serial == serial && it.online }
                    val dur = (System.currentTimeMillis() - started) / 1000
                    _state.compareAndSet(cur,
                        CaptureState.Done(file, bytes, dur, serial, autoStopped = !alive))
                }
            } catch (e: Exception) {
                destroyProc()
                val cur = _state.value as? CaptureState.Running
                _state.value = when {
                    // 会话未成立（启动失败如 ENOENT）或只有文件头：不能谎报"已保存"
                    cur == null || _bytes.value <= 128 -> {
                        cur?.file?.delete()
                        CaptureState.Idle
                    }
                    else -> CaptureState.Done(cur.file, _bytes.value, 0, cur.serial, autoStopped = true)
                }
            } finally {
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
    }

    /** 停止抓取（用户点击「结束并分享」/看门狗判死/断线联动）。幂等：仅 Running 态生效 */
    private fun stopCapture(userRequested: Boolean) {
        val cur = _state.value as? CaptureState.Running ?: return
        // CAS 语义防并发双收尾（watchdog 与用户停止同时到达）：只有第一个把 Running 换成 Done 的生效
        if (!_state.compareAndSet(cur, CaptureState.Done(
                cur.file, _bytes.value, (System.currentTimeMillis() - cur.startedAt) / 1000,
                cur.serial, !userRequested))
        ) {
            return
        }
        destroyProc()
    }

    private fun destroyProc() {
        proc?.let { p ->
            p.destroy()
            // destroy() 对卡死的进程不保证退出，2 秒不退再强杀
            val t = Thread { try { if (!p.waitFor(2, java.util.concurrent.TimeUnit.SECONDS)) p.destroyForcibly() } catch (ignored: InterruptedException) {} }
            t.start()
            try { t.join(2500) } catch (ignored: InterruptedException) {}
        }
        proc = null
    }

    override fun onDestroy() {
        destroyProc()
        scope.cancel()
        super.onDestroy()
    }

    /* ==================== 通知 ==================== */

    private fun createChannel() {
        val ch = NotificationChannel(CHANNEL_ID, "日志抓取", NotificationManager.IMPORTANCE_LOW)
        ch.description = "抓取日志时的常驻通知"
        getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    private fun buildNotification(text: String): Notification {
        val contentIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java).putExtra(EXTRA_FROM_NOTIF, true),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val stopIntent = PendingIntent.getService(
            this, 1,
            Intent(this, LogCaptureService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_notify_sync)
            .setContentTitle("正在抓取日志")
            .setContentText(text)
            .setContentIntent(contentIntent)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(
                null, "停止", stopIntent).build())
            .build().let { b ->
                notifBuilder = Notification.Builder.recoverBuilder(this, b)
                return b
            }
    }

    /** 外部（写入循环 tick）刷新通知文案 */
    fun updateNotification(text: String) {
        notifText = text
        val nm = getSystemService(NotificationManager::class.java)
        val b = notifBuilder ?: return
        b.setContentText(text)
        nm.notify(NOTIFICATION_ID, b.build())
    }

    private fun formatSize(bytes: Long): String = when {
        bytes >= 1024 * 1024 -> String.format(Locale.CHINA, "%.1f MB", bytes / 1048576.0)
        bytes >= 1024 -> String.format(Locale.CHINA, "%.0f KB", bytes / 1024.0)
        else -> "$bytes B"
    }
}
