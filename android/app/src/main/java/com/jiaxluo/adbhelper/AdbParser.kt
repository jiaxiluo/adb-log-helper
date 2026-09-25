/*
 * 文件名称 : AdbParser.kt
 * 功    能 : adb 输出解析纯函数集合（无 Android 依赖，可单测）。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M2 初版
 */
package com.jiaxluo.adbhelper

object AdbParser {

    /** adb devices 输出的一台设备 */
    data class Device(
        val serial: String,
        val state: String,          // device / offline / unauthorized / connecting…
    ) {
        val online: Boolean get() = state == "device"
        val tcpIp: Boolean get() = serial.contains(":")
    }

    /**
     * 解析 `adb devices -l` 输出。
     * 格式：`<serial>\t<state> key:value ...`，首行 List of devices attached，空行忽略
     */
    fun parseDevices(output: String): List<Device> {
        return output.lineSequence()
            .map { it.trim() }
            .filter { it.isNotEmpty() && !it.startsWith("List of devices") && !it.startsWith("*") }
            .mapNotNull { line ->
                val parts = line.split(Regex("\\s+"))
                if (parts.size >= 2 && parts[0].isNotEmpty()) {
                    Device(serial = parts[0], state = parts[1])
                } else {
                    null
                }
            }
            .toList()
    }

    /**
     * 解析 connect 命令输出 → (成功?, 用户可读原因)。
     * 判据顺序敏感：refused/timeout 必须在 "failed to connect" 之前匹配
     * （adb 实际输出形如 failed to connect to 'x:5555': Connection refused）。
     */
    fun parseConnect(output: String): Pair<Boolean, String> {
        val t = output.trim()
        return when {
            t.contains("already connected to") -> true to "已连接"
            t.contains("connected to") -> true to "连接成功"
            t.contains("Connection refused") || t.contains("connection refused") ->
                false to "连接被拒绝：设备 5555 端口未开放（电视需打开 ADB/网络调试开关）"
            t.contains("timeout") || t.contains("timed out") ->
                false to "连接超时：设备无响应（检查 IP、是否同一网络、路由器隔离）"
            t.contains("failed to connect") || t.contains("cannot connect") ->
                false to "连接失败：设备不在线（检查 ADB 调试开关、IP 是否正确、是否同一网络）"
            else -> false to if (t.isEmpty()) "连接失败（无返回）" else t
        }
    }

    /**
     * 解析 `getprop ro.product.model; getprop ro.product.brand;
     *       getprop ro.build.version.release` 三连输出 → 设备信息。
     * 任何一行为空显示 unknown。
     */
    data class PropInfo(val model: String, val brand: String, val android: String)

    fun parseProps(output: String): PropInfo {
        val lines = output.lines().map { it.trim() }.filter { it.isNotEmpty() }
        fun at(i: Int) = lines.getOrNull(i)?.takeIf { it.isNotEmpty() } ?: "unknown"
        return PropInfo(model = at(0), brand = at(1), android = at(2))
    }

    /**
     * 历史 IP 输入校验：允许 `a.b.c.d` 或 `a.b.c.d:port` 或 `host:port`。
     * 返回规范化后的 serial（缺省补 :5555），非法返回 null。
     */
    fun normalizeSerial(input: String): String? {
        val t = input.trim()
        if (t.isEmpty()) {
            return null
        }
        val (host, port) = if (t.contains(":")) {
            val i = t.lastIndexOf(':')
            t.substring(0, i) to t.substring(i + 1)
        } else {
            t to "5555"
        }
        if (host.isEmpty() || port.isEmpty()) {
            return null
        }
        if (!port.all { it.isDigit() } || port.length > 5 || port.toIntOrNull() !in 1..65535) {
            return null
        }
        // IPv4 严格校验：必须 4 段，每段数字 0~255；非 IPv4（域名）放行
        if (host.contains('.')) {
            val segs = host.split('.')
            if (segs.size != 4) {
                return null
            }
            for (s in segs) {
                if (s.isEmpty() || !s.all { it.isDigit() } || s.toIntOrNull() !in 0..255) {
                    return null
                }
            }
        }
        return "$host:$port"
    }

    /** install 输出 → 中文错误提示（保留原始码供高级用户查看） */
    fun translateInstallError(output: String): String? {
        val t = output
        return when {
            // 断连类判据放最前：可能与其他 Failure 码同时出现，断连是根因
            t.contains("device offline") || t.contains("device disconnected") ->
                "设备已断开连接，请重新连接后再试"
            t.contains("no devices/emulators found") || t.contains("device not found") ->
                "设备未连接，请重新连接后再试"
            t.contains("INSTALL_FAILED_VERSION_DOWNGRADE") ->
                "新包版本号比设备上已装的旧，需先在设备上卸载旧版"
            t.contains("INSTALL_FAILED_UPDATE_INCOMPATIBLE") ->
                "与设备上已装应用签名不一致，需先卸载原应用"
            t.contains("INSTALL_FAILED_INSUFFICIENT_STORAGE") ->
                "设备存储空间不足，请清理后重试"
            t.contains("INSTALL_FAILED_OLDER_SDK") || t.contains("INSTALL_FAILED_NO_MATCHING_ABIS") ->
                "该 APK 与设备系统版本/架构不匹配"
            t.contains("INSTALL_PARSE_FAILED_NO_CERTIFICATES") ->
                "APK 未签名或签名无效"
            t.contains("INSTALL_FAILED_INVALID_APK") || t.contains("INSTALL_PARSE_FAILED") ->
                "APK 文件损坏或不是有效的安装包"
            t.contains("INSTRUMENTATION_FAILED") -> "安装被设备策略拒绝"
            else -> null
        }
    }
}
