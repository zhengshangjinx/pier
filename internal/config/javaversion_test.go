package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 工程要求的 Java 版本要从 pom 里读准，包括常见的 ${java.version} 占位写法与 1.8 这种老写法。
func TestServiceJavaMajor(t *testing.T) {
	cases := []struct {
		what, root, module, want string
	}{
		{"properties 里的 java.version", `<properties><java.version>21</java.version></properties>`, "", "21"},
		{"release 指向占位符", `<properties><jdk>17</jdk><maven.compiler.release>${jdk}</maven.compiler.release></properties>`, "", "17"},
		{"老式 1.8 写法", `<properties><maven.compiler.source>1.8</maven.compiler.source></properties>`, "", "8"},
		{"子模块自己声明的优先", `<properties><java.version>17</java.version></properties>`, `<properties><java.version>21</java.version></properties>`, "21"},
		{"解不开的占位符不猜", `<properties><maven.compiler.release>${nope}</maven.compiler.release></properties>`, "", ""},
		{"什么都没写", `<project/>`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(tc.root), 0o644); err != nil {
				t.Fatal(err)
			}
			svc := &Service{Name: "x", Dir: dir, Kind: KindJava}
			if tc.module != "" {
				svc.Module = "shop-admin"
				if err := os.MkdirAll(filepath.Join(dir, svc.Module), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, svc.Module, "pom.xml"), []byte(tc.module), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := svc.JavaMajor(); got != tc.want {
				t.Errorf("JavaMajor() = %q，想要 %q", got, tc.want)
			}
		})
	}
}
