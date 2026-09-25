/*
 * 文件名称 : HistoryStore.kt
 * 功    能 : 连接历史持久化（JSON 行格式存 filesDir/history.json）。
 *            记录：serial + 品牌/型号/安卓版本 + 最近连接时间；置顶最近使用，上限 20 条。
 * 作    者 : jiaxiluo
 * 日    期 : 2026-09-24
 * 修    改 : V0.1.0-M2 初版
 */
package com.jiaxluo.adbhelper

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

data class HistoryEntry(
    val serial: String,          // ip:port
    val model: String,
    val brand: String,
    val android: String,
    val lastConnected: Long      // epoch millis
)

class HistoryStore(context: Context) {

    private val file: File = File(context.filesDir, "history.json")
    private val entries: MutableList<HistoryEntry> = load()

    companion object {
        private const val MAX_ENTRIES = 20
    }

    val all: List<HistoryEntry> get() = synchronized(entries) { entries.toList() }

    /** 连接成功后调用：命中则刷新信息+时间并置顶，未命中则头部插入 */
    fun record(serial: String, model: String, brand: String, android: String) = synchronized(entries) {
        entries.removeAll { it.serial == serial }
        entries.add(0, HistoryEntry(serial, model, brand, android, System.currentTimeMillis()))
        while (entries.size > MAX_ENTRIES) {
            entries.removeAt(entries.size - 1)
        }
        save()
    }

    fun remove(serial: String) = synchronized(entries) {
        entries.removeAll { it.serial == serial }
        save()
    }

    private fun load(): MutableList<HistoryEntry> {
        if (!file.exists()) {
            return mutableListOf()
        }
        return try {
            val arr = JSONArray(file.readText())
            (0 until arr.length()).mapNotNull { i ->
                val o = arr.optJSONObject(i) ?: return@mapNotNull null
                HistoryEntry(
                    serial = o.optString("serial"),
                    model = o.optString("model"),
                    brand = o.optString("brand"),
                    android = o.optString("android"),
                    lastConnected = o.optLong("ts")
                )
            }.toMutableList()
        } catch (e: Exception) {
            mutableListOf()
        }
    }

    private fun save() {
        try {
            val arr = JSONArray()
            for (e in entries) {
                arr.put(JSONObject().apply {
                    put("serial", e.serial)
                    put("model", e.model)
                    put("brand", e.brand)
                    put("android", e.android)
                    put("ts", e.lastConnected)
                })
            }
            // 原子写：写临时文件再改名，进程中途被杀不损坏存量历史
            val tmp = File(file.parentFile, file.name + ".tmp")
            tmp.writeText(arr.toString())
            if (!tmp.renameTo(file)) {
                file.writeText(arr.toString())
                tmp.delete()
            }
        } catch (ignored: Exception) {
            // 持久化失败不阻塞内存态
        }
    }
}
