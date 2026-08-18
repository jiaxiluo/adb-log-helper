package adb

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"adb-log-helper/internal/pathx"
	"adb-log-helper/internal/ui"
)

const (
	adbToolsDir       = "adb-tools"
	platformToolsName = "platform-tools"
	// platformToolsURL 是 Google 官方的 Windows 版 platform-tools 下载地址（联网安装用）
	platformToolsURL = "https://dl.google.com/android/repository/platform-tools-latest-windows.zip"
)

// InstallFromZip 从本地 ZIP 安装包安装 ADB platform-tools，并配置到用户 PATH。
// 安装顺序: 1. 检查本地已解压 → 2. 使用指定/自动查找的安装包解压
// 入参:
//   - zipPath: 安装包路径；空字符串表示自动在 exe 同目录查找支持的文件名
//
// 返回: ADB 可执行文件绝对路径, 错误信息
func InstallFromZip(zipPath string) (string, error) {
	// 检查本地是否已有解压安装（解压后 platform-tools/ 前缀被去掉，文件直接在 adb-tools/ 下）
	localAdb := filepath.Join(adbToolsDir, "adb.exe")
	if _, err := os.Stat(localAdb); err == nil {
		ui.Success("本地已存在 ADB，跳过安装")
		return configurePath(localAdb)
	}

	// 确定安装包来源：显式指定的路径优先；未指定时自动查找同目录安装包
	//   支持文件名: platform-tools.zip / platform-tools-latest-windows.zip / adb-tools.zip
	if zipPath == "" {
		zipPath = findLocalPackage()
	}
	if zipPath != "" {
		ui.Success("找到 ADB 安装包: %s", filepath.Base(zipPath))
		ui.Info("正在解压安装...")

		if err := extractPlatformTools(zipPath); err != nil {
			return "", fmt.Errorf("解压 ADB 安装包失败: %w", err)
		}

		ui.Success("解压完成")

		// 验证解压结果
		if _, err := os.Stat(localAdb); err != nil {
			return "", fmt.Errorf("解压后未找到 adb.exe，请检查安装包是否完整")
		}

		return configurePath(localAdb)
	}

	// 没有找到安装包，返回错误（由前端引导用户选择手动指定压缩包或联网下载）
	return "", fmt.Errorf(
		"未找到 ADB 安装包。可点击「选择本地压缩包」手动指定，" +
			"或点击「联网下载安装」从官方地址下载（约 8MB）")
}

// DownloadAndInstall 从 Google 官方地址联网下载 platform-tools 并安装。
// 下载完成后复用 InstallFromZip 解压安装，成功后删除下载的安装包。
// 入参:
//   - onProgress: 下载进度回调（percent 0-100，received/total 为字节数），可为 nil
//
// 返回: ADB 可执行文件绝对路径, 错误信息
func DownloadAndInstall(onProgress func(percent int, received, total int64)) (string, error) {
	// 下载目标：exe 同目录下的临时安装包（用完即删）
	exeDir := "."
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}
	dest := filepath.Join(exeDir, "platform-tools-latest-windows.zip")

	if err := downloadFile(platformToolsURL, dest, onProgress); err != nil {
		_ = os.Remove(dest) // 清理不完整的下载文件
		return "", fmt.Errorf("下载 ADB 安装包失败: %w", err)
	}

	// 解压安装（此时 dest 一定存在，InstallFromZip 会直接使用它）
	adbPath, err := InstallFromZip(dest)

	// 无论安装成败，下载的安装包都不再需要，删除保持目录整洁
	_ = os.Remove(dest)
	if err != nil {
		return "", err
	}
	return adbPath, nil
}

// downloadFile 从指定 URL 下载文件到本地路径（先写 .part 临时文件再改名，避免半截文件）。
// 入参:
//   - url:      下载地址
//   - destPath: 目标文件路径
//   - onProgress: 进度回调（可为 nil）
//
// 返回: 下载失败时的错误
func downloadFile(url string, destPath string, onProgress func(percent int, received, total int64)) error {
	// 客户端给足超时：安装包约 8MB，弱网环境放宽到 15 分钟
	client := &http.Client{Timeout: 15 * 60 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("无法访问下载地址（请检查网络连接）: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载地址返回异常状态: %s", resp.Status)
	}

	// 先写临时文件，下载完成后再改名，避免中途失败留下半截 zip
	partPath := destPath + ".part"
	outFile, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("创建下载临时文件失败: %w", err)
	}

	total := resp.ContentLength // 可能为 -1（服务端未返回长度，chunked 传输）
	var received int64
	const reportStep = 64 * 1024 // 进度回调的最小字节间隔，降低回调频率
	lastReported := int64(-1)    // 上次回调时的已下载字节数（-1 表示尚未回调过）
	buf := make([]byte, 64*1024) // 64KB 读缓冲

	report := func(force bool) {
		if onProgress == nil {
			return
		}
		// 节流：距上次回调不足 reportStep 字节且非强制（首块/结尾）时不回调
		if !force && received-lastReported < reportStep {
			return
		}
		lastReported = received
		percent := 0 // 未知总量时百分比无意义，前端按字节数展示
		if total > 0 {
			percent = int(received * 100 / total)
		}
		onProgress(percent, received, total)
	}

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := outFile.Write(buf[:n]); writeErr != nil {
				outFile.Close()
				_ = os.Remove(partPath)
				return fmt.Errorf("写入下载文件失败: %w", writeErr)
			}
			received += int64(n)
			report(lastReported < 0) // 首块强制回调一次，之后按字节间隔节流
		}
		if readErr != nil {
			outFile.Close()
			if readErr == io.EOF {
				break // 下载完成
			}
			_ = os.Remove(partPath)
			return fmt.Errorf("下载中断: %w", readErr)
		}
	}

	// 下载完成：强制补发一次最终进度，保证已知总量时百分比到达 100
	if total > 0 {
		lastReported = -1 // 重置节流状态，确保强制回调
	}
	report(true)

	if err := os.Rename(partPath, destPath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("保存下载文件失败: %w", err)
	}
	return nil
}

// findLocalPackage 在 exe 同目录下查找 ADB 安装包
func findLocalPackage() string {
	candidates := []string{
		"platform-tools.zip",
		"platform-tools-latest-windows.zip",
		"adb-tools.zip",
	}

	// 以可执行文件所在目录为基准查找安装包。
	// GUI 应用双击运行时，进程工作目录（cwd）可能不是 exe 所在目录，
	// 因此用 os.Executable 定位，避免找不到同目录下的安装包。
	exeDir := "."
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}

	for _, name := range candidates {
		p := filepath.Join(exeDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// configurePath 将 ADB 路径加入 PATH 环境变量并返回 adb 路径
func configurePath(adbPath string) (string, error) {
	absAdb, err := filepath.Abs(adbPath)
	if err != nil {
		return "", err
	}

	adbDir := filepath.Dir(absAdb)

	// 加入 PATH
	added, err := pathx.AddToUserPath(adbDir)
	if err != nil {
		ui.Warn("自动配置 PATH 失败: %v", err)
		ui.Warn("你可能需要手动将以下路径添加到系统环境变量 PATH 中:")
		ui.Warn("  %s", adbDir)
	} else if added {
		ui.Success("已将 ADB 添加到系统 PATH: %s", adbDir)
		ui.Info("PATH 变更将在新打开的终端窗口中生效")
	} else {
		ui.Success("PATH 中已包含 ADB 路径")
	}

	return absAdb, nil
}

// extractPlatformTools 解压 platform-tools ZIP 文件
func extractPlatformTools(zipPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	// 确保目标目录存在
	targetDir := filepath.Join(".", adbToolsDir)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	for _, f := range r.File {
		// 跳过顶层目录（ZIP 内通常是 platform-tools/xxx）
		name := f.Name
		if strings.HasPrefix(name, platformToolsName+"/") {
			name = name[len(platformToolsName)+1:]
		} else if strings.HasPrefix(name, platformToolsName+"\\") {
			name = name[len(platformToolsName)+1:]
		}

		if name == "" {
			continue
		}

		destPath := filepath.Join(targetDir, name)

		// 安全校验（防 zip slip）：清洗后的路径必须仍位于目标目录内。
		// 压缩包内若构造了 "../xxx" 之类的条目名，Join 清洗后会指向目标目录之外，
		// 直接中止解压，避免恶意包把文件写到任意位置（如覆盖系统文件）。
		cleanTarget := filepath.Clean(targetDir) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(destPath)+string(os.PathSeparator), cleanTarget) {
			return fmt.Errorf("压缩包内含非法路径，已中止解压: %s", f.Name)
		}

		// 创建目录
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, f.Mode()); err != nil {
				return err
			}
			continue
		}

		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		// 解压文件
		rc, err := f.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}

	return nil
}
