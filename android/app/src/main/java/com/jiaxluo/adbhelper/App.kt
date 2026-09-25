/*
 * 文件名称 : App.kt
 * 功    能 : Application 入口——进程级单例初始化 + 崩溃捕获（唯一安装点，
 *            替代原 Activity onCreate 安装；review #8：Activity 重建会叠加 handler
 *            导致一次崩溃写 N 份报告）。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 */
package com.jiaxluo.adbhelper

import android.app.Application

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        AppGraph.init(this)
        CrashReporter.install(this)
    }
}
