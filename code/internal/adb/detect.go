package adb

import (
	"os/exec"
	"strings"
)

// Detect 检测系统中是否已安装 ADB
// 返回: ADB 可执行文件路径, 是否找到, 错误信息
func Detect() (string, bool, error) {
	path, err := exec.LookPath("adb")
	if err != nil {
		return "", false, nil
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
