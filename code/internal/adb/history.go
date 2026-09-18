package adb

// ============================================================================
// 文件名称 : history.go
// 功    能 : 设备连接历史记录（最近断开的设备）。
//            跟随每次设备列表刷新更新：设备在线则刷新其最后在线时间，
//            设备消失则记一次断开时间；只保留最近 3 条已断开的记录，
//            持久化到程序目录 device_history.json，重启后仍可查看。
// ============================================================================

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// HistoryMax 是保留的"已断开设备"记录上限，超出丢弃最旧的。
const HistoryMax = 3

// historyFileName 是历史记录的持久化文件名（相对程序工作目录，即 exe 目录）。
const historyFileName = "device_history.json"

// HistoryEntry 是一台设备的历史记录条目。
// 字段说明：
//   - Serial:        设备序列号（USB 序列号或 ip:port）
//   - LastSeen:      最后一次在线时间
//   - DisconnectedAt: 断开时间（为 0 值表示当前在线）
type HistoryEntry struct {
	Serial         string    `json:"serial"`
	LastSeen       time.Time `json:"lastSeen"`
	DisconnectedAt time.Time `json:"disconnectedAt"`

	// absentPolls 是连续未在线的轮询次数（防抖计数，不持久化）。
	// 单次缺席（adb server 重启等瞬时抖动）不记为断开，连续 2 次才记
	absentPolls int `json:"-"`
}

// DeviceHistory 是设备历史跟踪器（并发安全）。
// GetDevices 轮询每 3 秒调用一次 Update，记录随之自动演进。
type DeviceHistory struct {
	mu      sync.Mutex
	entries map[string]*HistoryEntry // serial → 条目
	file    string                   // 持久化文件路径（可注入，测试用临时目录）
}

// NewDeviceHistory 创建跟踪器并加载程序目录的持久化文件（不存在时为空记录）。
func NewDeviceHistory() *DeviceHistory {
	return newDeviceHistoryAt(historyFileName)
}

// newDeviceHistoryAt 按指定路径创建跟踪器（单元测试注入临时目录用）。
func newDeviceHistoryAt(path string) *DeviceHistory {
	h := &DeviceHistory{
		entries: map[string]*HistoryEntry{},
		file:    path,
	}
	h.load()
	return h
}

// load 从持久化文件读取历史记录（失败静默，视为无历史）。
func (h *DeviceHistory) load() {
	data, err := os.ReadFile(h.file)
	if err != nil {
		return // 文件不存在或不可读：正常首次运行场景
	}
	var entries []*HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return // 内容损坏：丢弃旧历史，避免带病运行
	}
	for _, e := range entries {
		if e.Serial != "" {
			h.entries[e.Serial] = e
		}
	}
}

// persistLocked 把当前记录写回文件（失败静默——历史记录丢失不影响核心功能）。
// 写入顺序：先断开记录（最多 HistoryMax 条，这是用户要看的），再在线记录
// （按 LastSeen 倒序），总量截断到 10 条 —— 保证断开记录永不被在线记录挤掉。
func (h *DeviceHistory) persistLocked() {
	disconnected := h.recentLocked(HistoryMax)
	online := make([]*HistoryEntry, 0, len(h.entries))
	for _, e := range h.entries {
		if e.DisconnectedAt.IsZero() {
			online = append(online, e)
		}
	}
	sort.Slice(online, func(i, j int) bool {
		return online[i].LastSeen.After(online[j].LastSeen)
	})

	all := append(disconnected, online...)
	if len(all) > 10 {
		all = all[:10]
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(h.file, data, 0644)
}

// Update 用一次设备列表刷新结果更新历史记录。
// 在线设备：刷新 LastSeen 并清除断开标记（重连场景）；
// 消失设备：若此前在线，记一次 DisconnectedAt。
// 入参: serials 当前在线设备的序列号列表
func (h *DeviceHistory) Update(serials []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	dirty := false // 仅状态变化时落盘：3 秒轮询下避免无意义的持续磁盘写
	online := map[string]bool{}
	for _, s := range serials {
		online[s] = true
		if e, ok := h.entries[s]; ok {
			e.LastSeen = now
			e.absentPolls = 0 // 在线：清零防抖计数
			if !e.DisconnectedAt.IsZero() {
				e.DisconnectedAt = time.Time{} // 重连：清除断开标记
				dirty = true
			}
		} else {
			h.entries[s] = &HistoryEntry{Serial: s, LastSeen: now}
			dirty = true
		}
	}
	for s, e := range h.entries {
		if online[s] {
			continue
		}
		// 防抖：连续缺席 2 次才记断开 —— adb server 重启等造成的
		// 单次轮询抖动不会产生假历史记录
		e.absentPolls++
		if e.DisconnectedAt.IsZero() && e.absentPolls >= 2 {
			e.DisconnectedAt = now
			dirty = true
		}
	}
	if dirty {
		h.pruneLocked()
		h.persistLocked()
	}
}

// pruneLocked 清理超出规模的记录：断开记录只留最近 HistoryMax 条
// （多余的整条删除，防止 map 无限增长）。调用方需持有锁。
func (h *DeviceHistory) pruneLocked() {
	// 断开记录按断开时间倒序，只保留最近 HistoryMax 条
	disconnected := h.recentLocked(0) // 0 = 全部断开记录
	if len(disconnected) > HistoryMax {
		for _, e := range disconnected[HistoryMax:] {
			delete(h.entries, e.Serial)
		}
	}
}

// recentLocked 返回已断开的记录（按断开时间倒序）。
// limit <= 0 表示返回全部。调用方需持有锁。
func (h *DeviceHistory) recentLocked(limit int) []*HistoryEntry {
	var list []*HistoryEntry
	for _, e := range h.entries {
		if !e.DisconnectedAt.IsZero() {
			list = append(list, e)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		// 断开时刻相同时（快速连续断开可能落在同一时钟刻度），
		// 以序列号倒序决胜，保证输出顺序确定：map 遍历序随机 +
		// 非稳定排序会让同刻度的记录顺序每次不同
		if list[i].DisconnectedAt.Equal(list[j].DisconnectedAt) {
			return list[i].Serial > list[j].Serial
		}
		return list[i].DisconnectedAt.After(list[j].DisconnectedAt)
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

// MarkDisconnected 立即把一台设备记为已断开（用户主动断开后调用，
// 不等轮询防抖 —— 防抖只为过滤 adb server 抖动，主动断开是明确意图）。
// 设备不在记录中时忽略（从未在线过的设备无历史可记）。
// 入参: serial 设备序列号（TCP 设备为 ip:port）
func (h *DeviceHistory) MarkDisconnected(serial string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok := h.entries[serial]
	if !ok {
		return
	}
	e.absentPolls = 0 // 主动断开已记时间，防抖计数重置避免后续轮询重复处理
	if e.DisconnectedAt.IsZero() {
		e.DisconnectedAt = time.Now()
		h.pruneLocked()
		h.persistLocked()
	}
}

// Recent 返回最近断开的设备记录（最多 HistoryMax 条，最新在前）。
func (h *DeviceHistory) Recent() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.recentLocked(HistoryMax)
	out := make([]HistoryEntry, 0, len(list))
	for _, e := range list {
		out = append(out, *e)
	}
	return out
}
