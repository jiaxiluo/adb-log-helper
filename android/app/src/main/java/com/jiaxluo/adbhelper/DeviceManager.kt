/*
 * 文件名称 : DeviceManager.kt
 * 功    能 : 设备连接状态机（M2 核心）。
 *            状态：Disconnected / Connecting(serial) / Connected(serial+info)
 *            动作：connect / disconnect / refresh；后台线程执行 adb，状态经 Flow 发射。
 *            连接成功自动写历史；断线检测由 UI 层轮询 refresh 驱动。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M2 初版
 */
package com.jiaxluo.adbhelper

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/** 连接卡五态（对应 UI 原型：输入态/连接中/已连接，含失败原因） */
sealed class DeviceState {
    data object Disconnected : DeviceState()
    data class Connecting(val serial: String) : DeviceState()
    data class Connected(
        val serial: String,
        val model: String,
        val brand: String,
        val android: String
    ) : DeviceState()
}

/** 连接动作结果（供 UI 弹 Toast） */
data class ConnectResult(val ok: Boolean, val message: String)

class DeviceManager(private val context: Context) {

    private val _state = MutableStateFlow<DeviceState>(DeviceState.Disconnected)
    val state: StateFlow<DeviceState> = _state

    val history = HistoryStore(context)

    /** 用户手动断开过 → 冷启动/回前台时不再自动重连（尊重用户操作） */
    @Volatile
    private var userDisconnected = false

    /**
     * 状态写互斥（review #4）：connect/connectHistory/ensureConnection/disconnect 全部
     * 穿过同一把锁，杜绝"回前台自动重连 A 与手输连接 B 并发、慢者覆盖快者"的竞态
     * （该竞态会导致 UI 显示与实连设备不一致，装包/抓日志打到错误设备）。
     */
    private val stateMutex = Mutex()

    /** 当前 serial（未连接为 null） */
    val currentSerial: String?
        get() = (_state.value as? DeviceState.Connected)?.serial

    /**
     * 连接设备（输入可为 ip / ip:port）。
     * 流程：kill-server 复位 → connect → getprop 确认 → 写历史。
     */
    suspend fun connect(input: String): ConnectResult = withContext(Dispatchers.IO) {
        val serial = AdbParser.normalizeSerial(input)
            ?: return@withContext ConnectResult(false, "IP 格式不对（示例 192.168.1.84 或 192.168.1.84:5555）")

        stateMutex.withLock {
            _state.value = DeviceState.Connecting(serial)
            AdbCore.killServer(context)

            val (ok, msg) = AdbParser.parseConnect(AdbCore.run(context, 25, "connect", serial).output)
            if (!ok) {
                _state.value = DeviceState.Disconnected
                return@withContext ConnectResult(false, msg)
            }

            // 连上后读属性确认（解析失败也算连接成功，只是信息未知）
            val props = AdbParser.parseProps(
                AdbCore.run(
                    context, 20, "-s", serial, "shell",
                    "getprop ro.product.model; getprop ro.product.brand; getprop ro.build.version.release"
                ).output
            )
            _state.value = DeviceState.Connected(serial, props.model, props.brand, props.android)
            history.record(serial, props.model, props.brand, props.android)
            userDisconnected = false
            ConnectResult(true, "已连接 ${props.brand}/${props.model}")
        }
    }

    /** 从历史直连 */
    suspend fun connectHistory(entry: HistoryEntry): ConnectResult = withContext(Dispatchers.IO) {
        stateMutex.withLock {
            _state.value = DeviceState.Connecting(entry.serial)
            AdbCore.killServer(context)
            val (ok, msg) = AdbParser.parseConnect(AdbCore.run(context, 25, "connect", entry.serial).output)
            if (!ok) {
                _state.value = DeviceState.Disconnected
                return@withContext ConnectResult(false, msg)
            }
            _state.value = DeviceState.Connected(entry.serial, entry.model, entry.brand, entry.android)
            history.record(entry.serial, entry.model, entry.brand, entry.android)
            userDisconnected = false
            ConnectResult(true, "已连接 ${entry.brand}/${entry.model}")
        }
    }

    /** 主动断开 */
    suspend fun disconnect(): ConnectResult = withContext(Dispatchers.IO) {
        stateMutex.withLock {
            val serial = currentSerial
            if (serial != null) {
                AdbCore.run(context, 10, "disconnect", serial)
            }
            _state.value = DeviceState.Disconnected
            userDisconnected = true
            ConnectResult(true, "已断开")
        }
    }

    /** ensureConnection 结果：message=给用户的提示（null=无需提示）；dropped=确已掉线（联动清理） */
    data class EnsureResult(val message: String?, val dropped: Boolean)

    /**
     * 连接健康检查 + 自动恢复（UI 回前台/定时巡检调用）。
     *
     * 根因背景：adb 连接状态存在 adb server 进程内存里，App 退后台被系统冻结/回收时
     * server 会被连坐杀死；回前台 server 冷启动后设备列表为空 → 不能直接判死，
     * 先静默重连（TCP 亚秒级，电视已授权过 adbkey 不会重复弹授权框）。
     *
     * @param autoReconnect true=未连接态也尝试自动连"最近一次设备"（冷启动/回前台）；
     *                      手动断开过则不自动连
     */
    suspend fun ensureConnection(autoReconnect: Boolean = false): EnsureResult = withContext(Dispatchers.IO) {
        stateMutex.withLock {
        when (val cur = _state.value) {
            is DeviceState.Connected -> {
                val out = AdbCore.run(context, 15, "devices").output
                val still = AdbParser.parseDevices(out).any { it.serial == cur.serial && it.online }
                if (still) {
                    EnsureResult(null, false)
                } else {
                    // 设备真掉线，或 server 后台被杀后冷启（列表空）→ 静默重连一次再定论
                    val (ok, _) = AdbParser.parseConnect(AdbCore.run(context, 12, "connect", cur.serial).output)
                    if (ok) {
                        EnsureResult("已自动重连 ${cur.serial}", false)
                    } else {
                        _state.value = DeviceState.Disconnected
                        EnsureResult("设备 ${cur.serial} 已断开（自动重连失败）", true)
                    }
                }
            }

            is DeviceState.Disconnected -> {
                if (!autoReconnect || userDisconnected) {
                    EnsureResult(null, false)
                } else {
                    val latest = history.all.firstOrNull()
                    if (latest == null) {
                        EnsureResult(null, false)
                    } else {
                        _state.value = DeviceState.Connecting(latest.serial)
                        val (ok, _) = AdbParser.parseConnect(
                            AdbCore.run(context, 12, "connect", latest.serial).output
                        )
                        if (ok) {
                            _state.value = DeviceState.Connected(
                                latest.serial, latest.model, latest.brand, latest.android
                            )
                            history.record(latest.serial, latest.model, latest.brand, latest.android)
                            EnsureResult("已自动重连 ${latest.serial}", false)
                        } else {
                            // 静默失败：回未连接态但不弹窗打扰
                            _state.value = DeviceState.Disconnected
                            EnsureResult(null, false)
                        }
                    }
                }
            }

            is DeviceState.Connecting -> EnsureResult(null, false)
        }
        }
    }
}
