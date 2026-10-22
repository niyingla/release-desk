package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

const DefaultTheme = "night"

var Themes = []string{"night", "graphite", "purple", "forest", "light"}

type UiPreferences struct {
	Theme string `json:"theme"`
}

// LoadTheme 恢复独立于随机 HTTP 端口的本机主题偏好。
// 参数 dir：应用数据根目录，只读取本应用的界面偏好文件。
// 返回：有效主题或低亮度默认主题；损坏偏好不影响版本记录打开。
func LoadTheme(dir string) string {
	// 首次运行、文件损坏或旧版本未知值均回退，不覆盖用户业务数据。
	b, err := os.ReadFile(filepath.Join(dir, "ui-preferences.json"))
	// 外观文件不参与版本协议，解析失败不应让发版数据无法访问。
	var prefs UiPreferences
	if err != nil || json.Unmarshal(b, &prefs) != nil || !slices.Contains(Themes, prefs.Theme) {
		return DefaultTheme
	}
	// 合法主题由首屏 HTML 直接下发，避免随机端口丢失浏览器存储。
	return prefs.Theme
}
