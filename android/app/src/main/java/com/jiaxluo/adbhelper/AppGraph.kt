/*
 * 文件名称 : AppGraph.kt
 * 功    能 : 进程级单例容器（review #6：manager 挂 Activity 上旋转即丢状态）。
 *            DeviceManager/InstallManager 与进程同寿，Activity 重建（旋转/深色切换/
 *            分屏调整）只换观察者不换状态源，与 LogCaptureService 的 static 策略对齐。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 */
package com.jiaxluo.adbhelper

import android.app.Application

object AppGraph {
    private var app: Application? = null

    fun init(application: Application) {
        app = application
    }

    val deviceManager: DeviceManager by lazy {
        DeviceManager(app ?: throw IllegalStateException("AppGraph.init 未调用"))
    }

    val installManager: InstallManager by lazy {
        InstallManager(app ?: throw IllegalStateException("AppGraph.init 未调用"), deviceManager)
    }
}
