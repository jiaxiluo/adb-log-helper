package adb

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Detect 检测系统中是否已安装 ADB。
// 查找顺序：
//  1. 进程 PATH（LookPath）——系统安装 / 注册表 PATH 已生效的环境
//  2. exe 同目录的 adb-tools\adb.exe——本程序自装的 ADB 位置。
//     注册表用户 PATH 虽已写入，但未广播 WM_SETTINGCHANGE，双击重启后
//     GUI 进程继承的仍是旧 PATH，LookPath 找不到；不兜底会陷入
//     「重启→引导页→再安装」循环
//
// 返回: ADB 可执行文件路径, 是否找到, 错误信息
func Detect() (string, bool, error) {
	path, err := exec.LookPath("adb")
	if err != nil {
		path = localInstalledAdb()
		if path == "" {
			return "", false, nil
		}
	}

	// 验证 adb 是否可以正常运行
	output, err := hiddenCmd(path, "version").CombinedOutput()
	if err != nil {
		return "", false, nil
	}

	if !strings.Contains(string(output), "Android Debug Bridge") {
		return "", false, nil
	}

	return path, true, nil
}

// localInstalledAdb 返回本程序自装 ADB 的绝对路径（exe 同目录 adb-tools\adb.exe），
// 不存在时返回空字符串
func localInstalledAdb() string {
	exeDir := "."
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}
	localAdb := filepath.Join(exeDir, adbToolsDir, "adb.exe")
	if _, err := os.Stat(localAdb); err != nil {
		return ""
	}
	return localAdb
}
