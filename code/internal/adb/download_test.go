package adb

// ============================================================================
// 文件名称 : download_test.go
// 功    能 : downloadFile（联网安装的下载核心）的单元测试。
//            使用 net/http/httptest 在本地起 HTTP 服务模拟下载源，
//            不依赖外网，可离线重复运行。
// 覆盖点  :
//   1. 正常下载：内容完整、进度回调单调递增到 100、无 .part 残留
//   2. HTTP 错误（404）：返回错误，不留下半截文件
// 运行方式: 在项目根目录执行 go test ./internal/adb/
// ============================================================================

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestDownloadFileOK 验证正常下载流程：
// 文件内容完整落盘、进度回调最终到 100%、临时 .part 文件已清理。
func TestDownloadFileOK(t *testing.T) {
	// 构造约 300KB 的测试数据（大于 64KB 读缓冲，覆盖多轮读取）
	content := make([]byte, 300*1024)
	for i := range content {
		content[i] = byte(i % 251) // 非全零内容，便于校验完整性
	}

	// 本地 HTTP 服务模拟下载源（显式声明 Content-Length，走已知总长的进度路径）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	}))
	defer server.Close()

	// 下载到临时目录，避免污染源码目录
	dest := filepath.Join(t.TempDir(), "test-download.zip")

	// 收集进度回调，校验单调性
	var lastPercent = -1
	var percentAsc bool = true
	err := downloadFile(server.URL, dest, func(percent int, received, total int64) {
		if percent < lastPercent {
			percentAsc = false
		}
		lastPercent = percent
	})
	if err != nil {
		t.Fatalf("正常下载失败: %v", err)
	}

	// 校验文件内容与源数据一致
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("下载文件不可读: %v", err)
	}
	if len(got) != len(content) {
		t.Fatalf("文件大小不符: 期望 %d, 实际 %d", len(content), len(got))
	}
	for i := range content {
		if got[i] != content[i] {
			t.Fatalf("文件内容在第 %d 字节处不符", i)
		}
	}

	// 校验进度回调最终到达 100 且全程单调不减
	if lastPercent != 100 {
		t.Fatalf("进度回调未到达 100（最后为 %d）", lastPercent)
	}
	if !percentAsc {
		t.Fatal("进度回调出现回退（非单调递增）")
	}

	// 校验无 .part 临时文件残留
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal("下载完成后仍残留 .part 临时文件")
	}
}

// TestDownloadFileHTTPError 验证下载源返回 404 时：
// 返回错误、目标文件与 .part 临时文件均未落盘。
func TestDownloadFileHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "should-not-exist.zip")
	err := downloadFile(server.URL, dest, nil)
	if err == nil {
		t.Fatal("下载源返回 404 时应报错")
	}

	// 目标文件不应存在
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("下载失败时不应生成目标文件")
	}
	// 临时 .part 文件也不应残留
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal("下载失败时不应残留 .part 临时文件")
	}
}
