package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry 是往假产物里塞的一条。
type entry struct {
	name string
	body string
	// dir 为真时是一条目录项。
	dir bool
	// link 非空时是一条软链，内容就是它指向的路径。
	link string
	// mode 为 0 表示这条不写权限位——有的打包工具就是不给。
	mode os.FileMode
}

func writeZip(t *testing.T, path string, entries ...entry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.link != "":
			hdr.SetMode(os.ModeSymlink | 0o777)
		case e.dir:
			hdr.SetMode(os.ModeDir | 0o755)
		case e.mode != 0:
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("写 %s：%v", e.name, err)
		}
		body := e.body
		if e.link != "" {
			body = e.link
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("写 %s：%v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTarGz(t *testing.T, path string, entries ...entry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm())}
		switch {
		case e.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		case e.dir:
			h.Typeflag, h.Size = tar.TypeDir, 0
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("写 %s：%v", e.name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("写 %s：%v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// readFile 读一个解出来的文件。
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 两种格式解出来的东西必须一模一样：zip 与 tar 的差别只在怎么把条目读出来，
// 落盘那一侧是同一份代码，这条测试钉住的就是这一点。
func TestExtractBothFormats(t *testing.T) {
	entries := []entry{
		{name: "Pier.app/", dir: true},
		{name: "Pier.app/Contents/", dir: true},
		{name: "Pier.app/Contents/Info.plist", body: "版本 0.3.0", mode: 0o644},
		{name: "Pier.app/Contents/MacOS/pier", body: "#!/bin/sh\n", mode: 0o755},
		// 反斜杠路径（Windows 打的包里有）要按 / 认；中间那几个 .. 走完还在原地，
		// 这种不该被当成逃逸拦下来。
		{name: `Pier.app\Contents\..\Contents\MacOS\..\..\..\pier-gui`, body: "界面", mode: 0o755},
	}
	cases := []struct {
		name  string
		write func(t *testing.T, path string, entries ...entry)
		file  string
	}{
		{"zip", writeZip, "a.zip"},
		{"tar.gz", writeTarGz, "a.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out")
			archive := filepath.Join(t.TempDir(), c.file)
			c.write(t, archive, entries...)

			if err := Extract(archive, dst); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(dst, "Pier.app/Contents/Info.plist")); got != "版本 0.3.0" {
				t.Errorf("Info.plist 是 %q", got)
			}
			pier := filepath.Join(dst, "Pier.app/Contents/MacOS/pier")
			if got := readFile(t, pier); got != "#!/bin/sh\n" {
				t.Errorf("pier 是 %q", got)
			}
			if got := readFile(t, filepath.Join(dst, "pier-gui")); got != "界面" {
				t.Errorf("pier-gui 是 %q", got)
			}
			fi, err := os.Stat(filepath.Dir(pier))
			if err != nil || !fi.IsDir() {
				t.Errorf("中间那几层目录没建出来：%v", err)
			}
			// 可执行位要留住，否则换完之后那份二进制跑不起来。
			if fi, err := os.Stat(pier); err != nil || fi.Mode().Perm()&0o100 == 0 {
				t.Errorf("可执行位丢了：%v %v", fi.Mode(), err)
			}
		})
	}
}

// 解压目录是重建的：上一版解出来的树不能跟这一版混在一起，
// 两份产物里文件名对不上的时候，混着的那一份看着完整、其实一半是新的一半是旧的。
func TestExtractRebuildsDst(t *testing.T) {
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "上一次解出来的"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "a.zip")
	writeZip(t, archive, entry{name: "新的一份", body: "y", mode: 0o644})

	if err := Extract(archive, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "上一次解出来的")); !os.IsNotExist(err) {
		t.Error("上一次解出来的东西还在")
	}
	if got := readFile(t, filepath.Join(dst, "新的一份")); got != "y" {
		t.Errorf("新的一份是 %q", got)
	}
}

// 绝对路径与任何形式的 .. 逃逸一律拒绝。这些东西马上要被放进安装目录，
// 一份能往别处写文件的产物不能留。
func TestExtractRejectsEscapes(t *testing.T) {
	names := []string{
		"../外面",
		"a/../../外面",
		"/tmp/外面",
		`..\外面`,
		`a\..\..\外面`,
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dst := filepath.Join(root, "out")
			// 逃逸的落点就在 dst 的隔壁：真写出去的话这里会多一个文件。
			outside := filepath.Join(root, "外面")

			zipPath := filepath.Join(root, "a.zip")
			writeZip(t, zipPath, entry{name: name, body: "坏", mode: 0o644})
			if err := Extract(zipPath, dst); err == nil || !strings.Contains(err.Error(), "跑到外面去了") {
				t.Errorf("zip 里 %q 该被拒绝，得到 %v", name, err)
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("zip 里 %q 把文件写到外面去了", name)
			}

			tarPath := filepath.Join(root, "a.tar.gz")
			writeTarGz(t, tarPath, entry{name: name, body: "坏"})
			if err := Extract(tarPath, dst); err == nil || !strings.Contains(err.Error(), "跑到外面去了") {
				t.Errorf("tar 里 %q 该被拒绝，得到 %v", name, err)
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("tar 里 %q 把文件写到外面去了", name)
			}
		})
	}
}

// macOS 打包时顺手塞进来的元数据不是产物的一部分，跳过就好，不必当成错误。
func TestExtractSkipsMacMetadata(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, path string, entries ...entry)
		file  string
	}{
		{"zip", writeZip, "a.zip"},
		{"tar.gz", writeTarGz, "a.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out")
			archive := filepath.Join(t.TempDir(), c.file)
			c.write(t, archive,
				entry{name: "__MACOSX/._pier", body: "资源分叉", mode: 0o644},
				entry{name: "._pier", body: "资源分叉", mode: 0o644},
				entry{name: ".DS_Store", body: "访达的", mode: 0o644},
				entry{name: "Pier.app/.DS_Store", body: "访达的", mode: 0o644},
				entry{name: "pier", body: "真东西", mode: 0o755},
			)

			if err := Extract(archive, dst); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(dst, "pier")); got != "真东西" {
				t.Errorf("真东西没解出来：%q", got)
			}
			for _, junk := range []string{"__MACOSX", "._pier", ".DS_Store", "Pier.app"} {
				if _, err := os.Stat(filepath.Join(dst, junk)); !os.IsNotExist(err) {
					t.Errorf("%s 该被跳过", junk)
				}
			}
		})
	}
}

// 软链一概不收：我们自己打出来的包里没有，而一条指向外面的链能让后面写进去的
// 东西落到别处去——顺着链往安装目录外面写，是这条路线上最坏的一种可能。
func TestExtractRejectsLinks(t *testing.T) {
	t.Run("zip", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out")
		archive := filepath.Join(t.TempDir(), "a.zip")
		writeZip(t, archive, entry{name: "pier", link: "/usr/bin/true", mode: 0o755})
		if err := Extract(archive, dst); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
			t.Errorf("软链该被拒绝，得到 %v", err)
		}
	})
	t.Run("tar.gz", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out")
		archive := filepath.Join(t.TempDir(), "a.tar.gz")
		writeTarGz(t, archive, entry{name: "pier", link: "/usr/bin/true"})
		if err := Extract(archive, dst); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
			t.Errorf("软链该被拒绝，得到 %v", err)
		}
	})
}

// 格式只看后缀，不猜内容：猜错一次就是把一份压缩包当成目录树铺开。
func TestExtractUnknownFormat(t *testing.T) {
	err := Extract(filepath.Join(t.TempDir(), "a.7z"), filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "不认识的产物格式") {
		t.Fatalf("该说格式不认识，得到 %v", err)
	}
}

// 权限位要掩到 0777：产物里带着 setuid 位的话，解出来的东西会以一种
// 谁都没想到的身份运行。
func TestExtractMasksSetuid(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	archive := filepath.Join(t.TempDir(), "a.zip")
	writeZip(t, archive, entry{name: "pier", body: "x", mode: 0o755 | os.ModeSetuid | os.ModeSetgid})

	if err := Extract(archive, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "pier"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("解出来的东西带着特殊权限位：%v", fi.Mode())
	}
}

// 没写权限位的条目（有的打包工具就是不给）要落成能读的文件。
// 照 0 落下去得到的是一个谁都读不了的东西，换上去的二进制连启都不启不来。
func TestExtractDefaultsPermsWhenUnset(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(t *testing.T, path string, entries ...entry)
		file  string
	}{
		{"zip", writeZip, "a.zip"},
		{"tar.gz", writeTarGz, "a.tar.gz"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out")
			archive := filepath.Join(t.TempDir(), c.file)
			c.write(t, archive, entry{name: "pier", body: "x"})

			if err := Extract(archive, dst); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(filepath.Join(dst, "pier"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm()&0o400 == 0 {
				t.Errorf("权限位是 %v，该给自己留一个可读", fi.Mode().Perm())
			}
		})
	}
}

// 压缩包坏了要报错，不能解出半棵树还让人以为成了——后面那一步拿它去替换安装。
func TestExtractBrokenArchive(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	archive := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(archive, []byte("这不是压缩包"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, dst); err == nil || !strings.Contains(err.Error(), "打开压缩包失败") {
		t.Fatalf("该报打开失败，得到 %v", err)
	}
	archive = filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archive, []byte("这也不是"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, dst); err == nil || !strings.Contains(err.Error(), "打开压缩包失败") {
		t.Fatalf("该报打开失败，得到 %v", err)
	}
}
