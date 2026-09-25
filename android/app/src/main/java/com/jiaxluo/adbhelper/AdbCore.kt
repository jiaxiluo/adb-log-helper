/*
 * 文件名称 : AdbCore.kt
 * 功    能 : ADB 执行内核 —— 封装打包的 libadb.so 子进程调用。
 *            环境注入三件套（缺一不可，M1 真机验证结论）：
 *              HOME                → filesDir/.adb      （adbkey 密钥落位）
 *              ANDROID_ADB_LOG_PATH → …/server.log     （server 日志，避免写 /sdcard 被拒）
 *              LD_LIBRARY_PATH      → nativeLibraryDir  （Termux 动态版依赖解析）
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M2 初版（自 M1 MainActivity.runCmd 抽取）
 */
package com.jiaxluo.adbhelper

import android.content.Context
import java.io.File
import java.util.concurrent.TimeUnit

object AdbCore {

    /** adb 二进制绝对路径（安装时由 PM 解压到 nativeLibraryDir） */
    fun binaryPath(context: Context): String =
        File(context.applicationInfo.nativeLibraryDir, "libadb.so").absolutePath

    /** adb 私有工作目录（密钥 + server 日志） */
    fun homeDir(context: Context): File =
        File(context.filesDir, ".adb").apply { mkdirs() }

    /**
     * 执行 adb 命令并等待完成。
     * @param timeoutSec 超时秒数，超时强杀并返回 code=-1
     * @return CmdResult（code=0 表示成功；output 为合并的 stdout+stderr）
     */
    fun run(context: Context, timeoutSec: Long, vararg args: String): CmdResult {
        return try {
            val pb = ProcessBuilder(binaryPath(context), *args).apply {
                redirectErrorStream(true)
                val env = environment()
                env["HOME"] = homeDir(context).absolutePath
                env["TMPDIR"] = context.cacheDir.absolutePath
                env["ANDROID_ADB_LOG_PATH"] = File(homeDir(context), "server.log").absolutePath
                // 安卓 linker 对 exec 的二进制不搜索其自身所在目录，必须显式指路
                env["LD_LIBRARY_PATH"] = context.applicationInfo.nativeLibraryDir
            }
            val proc = pb.start()
            // StringBuffer（线程安全）：join 超时后主线程仍可能读 reader 未写完的缓冲
            val buf = StringBuffer()
            val reader = Thread {
                try {
                    buf.append(proc.inputStream.bufferedReader().use { it.readText() })
                } catch (ignored: Exception) {
                    // 进程被强杀时读流抛异常属预期
                }
            }
            reader.start()
            val done = try {
                proc.waitFor(timeoutSec, TimeUnit.SECONDS)
            } catch (e: InterruptedException) {
                // 线程被中断（协程取消）必须销毁子进程，否则 adb 孤儿最长挂 300s
                proc.destroyForcibly()
                throw e
            }
            if (!done) {
                proc.destroyForcibly()
                reader.join(1500)
                return CmdResult(-1, "[超时 ${timeoutSec}s] " + buf.toString())
            }
            // 进程已退出、流已 EOF，reader 收尾只剩拼串，短暂 join 必达；超时兜底读现有内容
            reader.join(2000)
            CmdResult(proc.exitValue(), buf.toString())
        } catch (e: Exception) {
            CmdResult(-1, "[异常] " + (e.message ?: e.javaClass.simpleName))
        }
    }

    /** 杀掉后台 adb server（复位残留连接/环境） */
    fun killServer(context: Context) {
        run(context, 10, "kill-server")
    }

    data class CmdResult(val code: Int, val output: String) {
        val ok: Boolean get() = code == 0
    }
}
