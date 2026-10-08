package pathx

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// AddToUserPath 将指定路径追加到用户级 PATH 环境变量（无需管理员权限）
// 返回值: 实际添加了路径返回 true，路径已存在返回 false，出错返回 error
func AddToUserPath(dirToAdd string) (bool, error) {
	absDir, err := filepath.Abs(dirToAdd)
	if err != nil {
		return false, err
	}

	// 打开用户环境变量注册表
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer key.Close()

	// 读取当前 Path 值（可能不存在）
	currentPath, _, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}

	// 检查路径是否已存在
	if isPathInList(absDir, currentPath) {
		// 已写入注册表，但当前进程环境未必包含它——注册表改于上次运行、
		// 且未广播 WM_SETTINGCHANGE 时，GUI 进程继承的还是旧 PATH。
		// 这里补齐进程环境，保证本次运行内即可用（幂等：已在进程 PATH 中则跳过）
		if procPath := os.Getenv("PATH"); !isPathInList(absDir, procPath) {
			_ = os.Setenv("PATH", absDir+";"+procPath)
		}
		return false, nil
	}

	// 追加路径
	var newPath string
	if currentPath == "" {
		newPath = absDir
	} else {
		newPath = currentPath + ";" + absDir
	}

	// 写回注册表
	if err := key.SetStringValue("Path", newPath); err != nil {
		return false, err
	}

	// 更新当前进程的环境变量，使本次运行即可使用
	if err := os.Setenv("PATH", newPath+";"+os.Getenv("PATH")); err != nil {
		return true, err // PATH 已写入注册表，但进程环境更新失败不影响
	}

	return true, nil
}

// isPathInList 检查目标路径是否已存在于 PATH 列表中（忽略大小写）
func isPathInList(target string, pathList string) bool {
	if pathList == "" {
		return false
	}
	target = strings.ToLower(filepath.Clean(target))
	entries := strings.Split(pathList, ";")
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.ToLower(filepath.Clean(entry)) == target {
			return true
		}
	}
	return false
}
