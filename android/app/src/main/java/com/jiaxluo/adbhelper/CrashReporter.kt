/*
 * 文件名称 : CrashReporter.kt
 * 功    能 : 全局崩溃捕获（用户反馈"多次点击 APP 崩溃退出"后立项）。
 *            崩溃瞬间把堆栈+设备信息写入 filesDir/crash/crash_<时间戳>.txt，
 *            最多保留 5 份（新的挤掉旧的）；下次启动 MainActivity 读取最新一份
 *            供 UI 展示与一键分享。崩溃后仍交回系统默认处理（保持崩溃行为一致，
 *            只多落一份现场）。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M5 初版
 */
package com.jiaxluo.adbhelper

import android.content.Context
import android.os.Build
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

object CrashReporter {

    private const val DIR = "crash"
    private const val KEEP = 5

    /** 幂等护栏（review #8）：重复 install 不叠加 handler */
    @Volatile
    private var installed = false

    /** 安装全局拦截器（Application onCreate 调用一次） */
    fun install(context: Context) {
        if (installed) {
            return
        }
        installed = true
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            try {
                save(context.applicationContext, thread, throwable)
            } catch (ignored: Exception) {
                // 崩溃现场落盘失败不能再抛，交回系统
            }
            previous?.uncaughtException(thread, throwable)
        }
    }

    /** 崩溃目录 */
    fun crashDir(context: Context): File =
        File(context.filesDir, DIR).apply { mkdirs() }

    /** 最新一份崩溃报告（无则 null） */
    fun latest(context: Context): File? =
        crashDir(context).listFiles { f -> f.name.startsWith("crash_") }
            ?.maxByOrNull { it.name }

    /** 分享后/忽略后清除记录 */
    fun clear(context: Context) {
        crashDir(context).listFiles()?.forEach { it.delete() }
    }

    private fun save(context: Context, thread: Thread, throwable: Throwable) {
        val dir = crashDir(context)
        val stamp = SimpleDateFormat("yyyyMMdd_HHmmss", Locale.CHINA).format(Date())
        val out = File(dir, "crash_$stamp.txt")
        val sw = StringWriter()
        throwable.printStackTrace(PrintWriter(sw))
        out.writeText(
            buildString {
                appendLine("时间: " + SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.CHINA).format(Date()))
                appendLine("线程: " + thread.name)
                appendLine("机型: " + Build.MANUFACTURER + " " + Build.MODEL)
                appendLine("系统: Android " + Build.VERSION.RELEASE + " (API " + Build.VERSION.SDK_INT + ")")
                appendLine("版本: " + AppInfo.VERSION)
                appendLine()
                appendLine(sw.toString())
            }
        )
        // 只留最近 KEEP 份
        dir.listFiles { f -> f.name.startsWith("crash_") }
            ?.sortedByDescending { it.name }
            ?.drop(KEEP)
            ?.forEach { it.delete() }
    }
}
