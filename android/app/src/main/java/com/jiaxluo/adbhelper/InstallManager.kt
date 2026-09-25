/*
 * 文件名称 : InstallManager.kt（重构版）
 * 功    能 : APK 安装状态机（M3）。
 *            设计要点：
 *              1. Picked/Fail 态共享同一个缓存副本（cacheFile 始终持有引用，杜绝路径重拼），
 *                 Fail → retry 直接复用副本；Done/换文件/断开联动时删除副本
 *              2. content:// 必须先拷缓存（adb 子进程读不了 SAF），PK 魔数粗校验拦截误选
 *              3. install 超时 300s（大 APK + 慢 WiFi），断连类错误在 Parser 最优先翻译
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M3 初版
 */
package com.jiaxluo.adbhelper

import android.content.Context
import android.net.Uri
import android.provider.OpenableColumns
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import java.io.File

/** 安装卡状态（对应原型：选择文件 → 开始安装 → 进度 → 成功/失败+重试） */
sealed class InstallState {
    data object Idle : InstallState()
    data class Picked(val fileName: String, val sizeBytes: Long, val cacheFile: File) : InstallState()
    data class Installing(val fileName: String) : InstallState()
    data class Done(val fileName: String, val displayName: String) : InstallState()
    data class Fail(
        val fileName: String,
        val reason: String,      // 中文原因
        val raw: String,         // 设备端原始错误单行（灰色小字）
        val cacheFile: File      // 保留副本供重试
    ) : InstallState()
}

class InstallManager(
    private val context: Context,
    private val deviceManager: DeviceManager
) {

    private val _state = MutableStateFlow<InstallState>(InstallState.Idle)
    val state: StateFlow<InstallState> = _state

    /** 正在安装中（供断开联动判断） */
    val busy: Boolean get() = _state.value is InstallState.Installing

    /**
     * SAF 回来的 Uri → 拷贝到缓存目录并进入 Picked 态。
     * @return null=成功；非 null=错误文案
     */
    suspend fun pick(uri: Uri): String? = withContext(Dispatchers.IO) {
        // 换文件场景：删除上一个副本
        cleanupCache(_state.value)

        val name = queryFileName(uri) ?: "app.apk"
        val dst = File(context.cacheDir, "install_$name")
        try {
            context.contentResolver.openInputStream(uri)?.use { input ->
                dst.outputStream().use { output ->
                    input.copyTo(output, bufferSize = 256 * 1024)
                }
            } ?: return@withContext "无法读取所选文件"
        } catch (e: Exception) {
            dst.delete()
            return@withContext "读取文件失败：" + (e.message ?: e.javaClass.simpleName)
        }
        // 粗校验：APK 本质是 zip，PK 魔数不符直接拒绝（防止把图片/文档当 APK 推给设备）
        // 注意：readNBytes 是 API 33+，minSdk 26 不能用（安卓 8~12 会 NoSuchMethodError）
        val head = ByteArray(2)
        dst.inputStream().use { input ->
            var read = 0
            while (read < 2) {
                val n = input.read(head, read, 2 - read)
                if (n < 0) {
                    break
                }
                read += n
            }
        }
        if (head[0] != 'P'.code.toByte() || head[1] != 'K'.code.toByte()) {
            dst.delete()
            return@withContext "所选文件不是有效的 APK"
        }
        _state.value = InstallState.Picked(name, dst.length(), dst)
        null
    }

    /** 开始安装（须处于 Picked 态且设备已连接） */
    suspend fun install(): InstallResult = withContext(Dispatchers.IO) {
        val picked = _state.value as? InstallState.Picked
            ?: return@withContext InstallResult(false, "请先选择 APK 文件")
        val serial = deviceManager.currentSerial
            ?: return@withContext InstallResult(false, "设备未连接")

        _state.value = InstallState.Installing(picked.fileName)
        // -r 覆盖安装保留数据；大 APK + 慢 WiFi 给足 300s
        val r = AdbCore.run(context, 300, "-s", serial, "install", "-r", picked.cacheFile.absolutePath)
        val out = r.output
        return@withContext if (out.contains("Success")) {
            picked.cacheFile.delete()
            _state.value = InstallState.Done(picked.fileName, picked.fileName)
            InstallResult(true, "安装成功")
        } else {
            val cn = AdbParser.translateInstallError(out) ?: "安装失败（未知原因）"
            // 副本保留在 Fail 态里供「重试」
            _state.value = InstallState.Fail(picked.fileName, cn, rawSummary(out), picked.cacheFile)
            InstallResult(false, cn)
        }
    }

    /** 失败态的「重试」：直接复用缓存副本再走安装 */
    suspend fun retry(): InstallResult {
        val fail = _state.value as? InstallState.Fail
            ?: return InstallResult(false, "无可重试的安装")
        if (!fail.cacheFile.exists()) {
            _state.value = InstallState.Idle
            return InstallResult(false, "安装文件已失效，请重新选择")
        }
        _state.value = InstallState.Picked(fail.fileName, fail.cacheFile.length(), fail.cacheFile)
        return install()
    }

    /** 重置（换文件/断开联动）：清理缓存副本回 Idle */
    fun reset() {
        cleanupCache(_state.value)
        _state.value = InstallState.Idle
    }

    /* ---------- 内部工具 ---------- */

    private fun cleanupCache(state: InstallState) {
        when (state) {
            is InstallState.Picked -> state.cacheFile.delete()
            is InstallState.Fail -> state.cacheFile.delete()
            else -> Unit
        }
    }

    private fun queryFileName(uri: Uri): String? {
        context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)
            ?.use { c ->
                if (c.moveToFirst()) {
                    val name = c.getString(0)
                    if (!name.isNullOrBlank()) {
                        return name
                    }
                }
            }
        return uri.lastPathSegment?.substringAfterLast('/')
    }

    /** 失败原始输出压缩成单行（截断，供灰色小字展示原始码） */
    private fun rawSummary(output: String): String =
        output.lineSequence()
            .map { it.trim() }
            .filter { it.contains("Failure") || it.contains("INSTALL_FAILED") }
            .firstOrNull() ?: output.take(120).replace("\n", " ")

    data class InstallResult(val ok: Boolean, val message: String)
}
