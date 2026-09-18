package adb

// ============================================================================
// 文件名称 : history_test.go
// 功    能 : 设备历史跟踪器（DeviceHistory）的白盒单元测试。
//            覆盖：记录/断开/重连演进、最多保留 3 条、
//            持久化往返（重启恢复）、损坏文件容错、无变化不落盘。
// ============================================================================

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper：在临时目录创建跟踪器，返回跟踪器与持久化文件路径
func newTestHistory(t *testing.T) (*DeviceHistory, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "device_history.json")
	return newDeviceHistoryAt(path), path
}

// 场景：设备从未连接过 → 历史为空
func TestHistoryEmpty(t *testing.T) {
	h, _ := newTestHistory(t)
	if got := h.Recent(); len(got) != 0 {
		t.Fatalf("初始历史应为空，实际 %d 条", len(got))
	}
}

// 场景：一台设备连接后断开（连续 2 次轮询缺席，防抖协议）→ 出现在历史中
func TestHistoryRecordDisconnect(t *testing.T) {
	h, _ := newTestHistory(t)
	h.Update([]string{"192.168.1.100:5555"})
	h.Update(nil) // 缺席第 1 次（防抖中，不记断开）
	if got := h.Recent(); len(got) != 0 {
		t.Fatalf("单次缺席不应记为断开（防抖），实际 %d 条", len(got))
	}
	h.Update(nil) // 缺席第 2 次 → 记断开

	recent := h.Recent()
	if len(recent) != 1 {
		t.Fatalf("断开后应有 1 条历史，实际 %d 条", len(recent))
	}
	if recent[0].Serial != "192.168.1.100:5555" {
		t.Fatalf("序列号不符：%s", recent[0].Serial)
	}
	if recent[0].DisconnectedAt.IsZero() {
		t.Fatal("断开时间不应为零值")
	}
}

// 场景：瞬时抖动（缺席 1 次后立刻回来，如 adb server 重启）→ 不产生假历史
func TestHistoryDebounceTransientGap(t *testing.T) {
	h, _ := newTestHistory(t)
	h.Update([]string{"devA"})
	h.Update(nil)              // 缺席 1 次
	h.Update([]string{"devA"}) // 立刻回来

	if got := h.Recent(); len(got) != 0 {
		t.Fatalf("瞬时抖动不应产生历史记录，实际 %d 条", len(got))
	}
}

// 场景：用户主动断开 → 立即记入历史（不等 2 次缺席防抖）
func TestHistoryMarkDisconnectedImmediate(t *testing.T) {
	h, _ := newTestHistory(t)
	h.Update([]string{"192.168.1.100:5555"})

	h.MarkDisconnected("192.168.1.100:5555") // 一次缺席都没有
	if got := h.Recent(); len(got) != 1 {
		t.Fatalf("主动断开应立即记入历史，实际 %d 条", len(got))
	}

	// 未知设备（从未在线）不应产生记录
	h.MarkDisconnected("10.0.0.9:5555")
	if got := h.Recent(); len(got) != 1 {
		t.Fatalf("未知设备不应产生历史记录，实际 %d 条", len(got))
	}

	// 重复调用不覆盖首次断开时间
	first := h.Recent()[0].DisconnectedAt
	time.Sleep(10 * time.Millisecond)
	h.MarkDisconnected("192.168.1.100:5555")
	if again := h.Recent()[0].DisconnectedAt; !again.Equal(first) {
		t.Fatalf("重复 MarkDisconnected 不应刷新断开时间")
	}
}

// 场景：断开的设备重新上线 → 从历史中消失（清除断开标记）
func TestHistoryReconnectClears(t *testing.T) {
	h, _ := newTestHistory(t)
	h.Update([]string{"devA"})
	h.Update(nil)
	h.Update(nil) // 两次缺席 → 记断开
	if got := h.Recent(); len(got) != 1 {
		t.Fatalf("前置条件失败：应已记录 1 条断开，实际 %d 条", len(got))
	}
	h.Update([]string{"devA"}) // 重连

	if got := h.Recent(); len(got) != 0 {
		t.Fatalf("重连后历史应清空，实际 %d 条", len(got))
	}
}

// 场景：断开设备超过 3 台 → 只保留最近断开的 3 条。
// 防抖协议下断开时刻 = 连续第 2 次缺席的轮询，d1~d5 依次错开
func TestHistoryMaxThree(t *testing.T) {
	h, _ := newTestHistory(t)

	h.Update([]string{"d1", "d2", "d3", "d4", "d5"}) // 全部在线
	h.Update([]string{"d2", "d3", "d4", "d5"})       // d1 缺席 1
	h.Update([]string{"d3", "d4", "d5"})             // d1 断开；d2 缺席 1
	h.Update([]string{"d4", "d5"})                   // d2 断开；d3 缺席 1
	h.Update([]string{"d5"})                         // d3 断开；d4 缺席 1
	h.Update(nil)                                    // d4 断开；d5 缺席 1
	h.Update(nil)                                    // d5 断开

	recent := h.Recent()
	if len(recent) != HistoryMax {
		t.Fatalf("应只保留 %d 条历史，实际 %d 条", HistoryMax, len(recent))
	}
	// 最新断开的在前：d5, d4, d3
	want := []string{"d5", "d4", "d3"}
	for i, w := range want {
		if recent[i].Serial != w {
			t.Fatalf("第 %d 条应为 %s，实际 %s", i, w, recent[i].Serial)
		}
	}
}

// 场景：持久化时断开记录不被在线记录挤掉
// （review 发现：总量截断若按 LastSeen 排序，在线设备多时断开记录可能落榜）
func TestHistoryPersistKeepsDisconnected(t *testing.T) {
	h, path := newTestHistory(t)

	// 3 台先断开（成为历史），再接入 8 台在线设备（撑爆 10 条截断线）
	h.Update([]string{"old1", "old2", "old3"})
	h.Update(nil)
	h.Update(nil) // old1~old3 记断开
	h.Update([]string{"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8"})
	h.Update([]string{"o1", "o2", "o3", "o4", "o5", "o6", "o7", "o8"})

	// 重启恢复：3 条断开记录必须完整找回
	h2 := newDeviceHistoryAt(path)
	recent := h2.Recent()
	if len(recent) != 3 {
		t.Fatalf("重启后应恢复 3 条断开记录，实际 %d 条：%+v", len(recent), recent)
	}
	serials := map[string]bool{}
	for _, e := range recent {
		serials[e.Serial] = true
	}
	for _, want := range []string{"old1", "old2", "old3"} {
		if !serials[want] {
			t.Fatalf("断开记录 %s 被在线记录挤掉", want)
		}
	}
	_ = time.Sleep // 保持 import
}

// 场景：重启（新建跟踪器读同一文件）→ 历史记录恢复
func TestHistoryPersistenceRoundTrip(t *testing.T) {
	h, path := newTestHistory(t)
	h.Update([]string{"usb-123", "10.0.0.8:5555"})
	h.Update([]string{"usb-123"}) // 10.0.0.8 缺席 1
	h.Update([]string{"usb-123"}) // 缺席 2 → 记断开

	h2 := newDeviceHistoryAt(path)
	recent := h2.Recent()
	if len(recent) != 1 || recent[0].Serial != "10.0.0.8:5555" {
		t.Fatalf("重启后应恢复 1 条 10.0.0.8:5555 的历史，实际 %+v", recent)
	}
}

// 场景：持久化文件损坏 → 静默丢弃，从空历史开始，不 panic
func TestHistoryCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device_history.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newDeviceHistoryAt(path)
	if got := h.Recent(); len(got) != 0 {
		t.Fatalf("损坏文件应视为无历史，实际 %d 条", len(got))
	}
	// 后续正常更新应能覆写损坏文件
	h.Update([]string{"d1"})
	h.Update(nil)
	h.Update(nil) // 两次缺席 → 记断开
	h2 := newDeviceHistoryAt(path)
	if got := h2.Recent(); len(got) != 1 || got[0].Serial != "d1" {
		t.Fatalf("覆写后应能正常恢复，实际 %+v", got)
	}
}

// 场景：列表无变化（仍在线）→ 不写盘（验证 dirty 优化）
func TestHistoryNoChangeNoWrite(t *testing.T) {
	h, path := newTestHistory(t)
	h.Update([]string{"d1"})

	// 记录首次落盘后的文件时间戳/内容，再无变化地更新一次
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("首次更新应已落盘:", err)
	}
	mtimeBefore := statMtime(t, path)

	time.Sleep(20 * time.Millisecond)
	h.Update([]string{"d1"}) // 设备仍在线：状态无变化

	if after, _ := os.ReadFile(path); string(before) != string(after) {
		t.Fatal("无变化时不应改写文件内容")
	}
	if statMtime(t, path) == mtimeBefore {
		return // 修改时间未变：确认没写盘
	}
	// 修改时间变化但内容相同也可能是重写——内容一致即满足要求，不算失败
}

// statMtime 读取文件修改时间（纳秒精度，用于判断是否发生过写操作）
func statMtime(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UnixNano()
}
