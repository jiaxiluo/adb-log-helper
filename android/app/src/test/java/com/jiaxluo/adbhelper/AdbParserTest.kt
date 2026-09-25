/*
 * 文件名称 : AdbParserTest.kt
 * 功    能 : AdbParser 纯函数单元测试（JVM 本地跑，无需设备）。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M2 初版
 */
package com.jiaxluo.adbhelper

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AdbParserTest {

    /* ---------- parseDevices ---------- */

    @Test
    fun parseDevices_normal() {
        val out = """
            List of devices attached
            192.168.1.84:5555	device product:xxx model:7T646
            emulator-5554	device
            192.168.1.9:5555	offline

        """.trimIndent()
        val list = AdbParser.parseDevices(out)
        assertEquals(3, list.size)
        assertEquals("192.168.1.84:5555", list[0].serial)
        assertTrue(list[0].online)
        assertEquals("offline", list[2].state)
        assertFalse(list[2].online)
    }

    @Test
    fun parseDevices_empty() {
        val out = "List of devices attached\n\n* daemon not running\n"
        assertTrue(AdbParser.parseDevices(out).isEmpty())
    }

    /* ---------- parseConnect ---------- */

    @Test
    fun connect_success() {
        val (ok, _) = AdbParser.parseConnect("connected to 192.168.1.84:5555")
        assertTrue(ok)
    }

    @Test
    fun connect_already() {
        val (ok, msg) = AdbParser.parseConnect("already connected to 192.168.1.84:5555")
        assertTrue(ok)
        assertEquals("已连接", msg)
    }

    @Test
    fun connect_refused() {
        val (ok, msg) = AdbParser.parseConnect("failed to connect to '192.168.1.84:5555': Connection refused")
        assertFalse(ok)
        assertTrue(msg.contains("5555 端口未开放"))
    }

    @Test
    fun connect_timeout() {
        val (ok, msg) = AdbParser.parseConnect("failed to connect to '192.168.1.9:5555': timeout")
        assertFalse(ok)
        assertTrue(msg.contains("超时"))
    }

    /* ---------- parseProps ---------- */

    @Test
    fun props_normal() {
        val p = AdbParser.parseProps("7T646_A4FP\nHEMILE\n13")
        assertEquals("7T646_A4FP", p.model)
        assertEquals("HEMILE", p.brand)
        assertEquals("13", p.android)
    }

    @Test
    fun props_emptyLines() {
        // 模拟 getprop 三连输出中夹杂空行（行序：model/brand/android）
        val p = AdbParser.parseProps("\nmodel-x\n\nbrand-y\n\n11\n")
        assertEquals("model-x", p.model)
        assertEquals("brand-y", p.brand)
        assertEquals("11", p.android)
    }

    /* ---------- normalizeSerial ---------- */

    @Test
    fun serial_bareIp() {
        assertEquals("192.168.1.84:5555", AdbParser.normalizeSerial("192.168.1.84"))
    }

    @Test
    fun serial_withPort() {
        assertEquals("192.168.1.84:5556", AdbParser.normalizeSerial("192.168.1.84:5556"))
    }

    @Test
    fun serial_spaces() {
        assertEquals("10.0.0.2:5555", AdbParser.normalizeSerial(" 10.0.0.2 "))
    }

    @Test
    fun serial_badIp() {
        assertNull(AdbParser.normalizeSerial("999.1.1.1"))
        assertNull(AdbParser.normalizeSerial("1.2.3"))
        assertNull(AdbParser.normalizeSerial("a.b.c.d:xyz"))
        assertNull(AdbParser.normalizeSerial(""))
        assertNull(AdbParser.normalizeSerial("1.2.3.4:99999"))
        assertNull(AdbParser.normalizeSerial("1.2.3.4:0"))
    }

    /* ---------- translateInstallError ---------- */

    @Test
    fun install_downgrade() {
        val msg = AdbParser.translateInstallError(
            "Failure [INSTALL_FAILED_VERSION_DOWNGRADE]")
        assertTrue(msg!!.contains("版本号"))
    }

    @Test
    fun install_abis() {
        val msg = AdbParser.translateInstallError(
            "Failure [INSTALL_FAILED_NO_MATCHING_ABIS: Failed to extract native libraries]")
        assertTrue(msg!!.contains("不匹配"))
    }

    @Test
    fun install_unknown() {
        assertNull(AdbParser.translateInstallError("some random output"))
    }
}
