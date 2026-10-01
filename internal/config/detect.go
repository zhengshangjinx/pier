package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// 用于识别项目类型的标志文件。顺序即优先级：一个目录同时有多种标志时，
// 先命中者胜出，因此把「服务端」特征排在「前端」之前。
var kindMarkers = []struct {
	kind  string
	files []string
}{
	{KindGo, []string{"go.mod"}},
	{KindJava, []string{"pom.xml", "build.gradle", "build.gradle.kts"}},
	{KindPython, []string{"pyproject.toml", "requirements.txt", "main.py"}},
	{KindNode, []string{"package.json"}},
}

// DetectKind 按目录内容判断项目类型。
func DetectKind(dir string) (string, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("目录不存在：%s", dir)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("不是目录：%s", dir)
	}

	for _, m := range kindMarkers {
		for _, f := range m.files {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return m.kind, nil
			}
		}
	}
	return "", fmt.Errorf("无法识别项目类型（未找到 go.mod / pom.xml / package.json / pyproject.toml）：%s", dir)
}
