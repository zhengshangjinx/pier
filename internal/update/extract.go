package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// maxExtractBytes 是解压总量的上限。产物是几十兆，这里留了两个数量级的余量：
	// 它挡的不是正常产物，是一份被换掉的包把磁盘塞满。
	maxExtractBytes = 2 << 30
	// maxExtractFiles 是条目数上限，挡的是「一大批空文件把小文件系统占满 inode」。
	maxExtractFiles = 50000
)

// Extract 把产物解到 dst 目录。
//
// 格式按文件名后缀认（.zip / .tar.gz），不看内容猜：猜错一次就是把一份压缩包
// 当成目录树铺开。dst 会先整个重建一次，解出来的东西随后要被拿去替换安装——
// 顺序永远是先校验和（Download 里做完了）再解压，解出来的字节马上就会被执行。
func Extract(archive, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("清理解压目录失败：%w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("创建解压目录失败：%w", err)
	}
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dst)
	case strings.HasSuffix(archive, ".tar.gz"):
		return extractTarGz(archive, dst)
	default:
		return fmt.Errorf("不认识的产物格式：%s", filepath.Base(archive))
	}
}

// unpacker 是两种格式共用的落盘那一半：路径检查、跳过垃圾、总量与条目数封顶。
//
// zip 与 tar 的差别只在「怎么把一个个条目读出来」，落盘这一侧必须一模一样——
// 各写一份的话，安全检查迟早只有一边做全。
type unpacker struct {
	root  string
	files int
	bytes int64
}

// path 算出条目该落在哪。skip 为真表示这一条不用管（目录项在 zip 里也是条目）。
func (u *unpacker) path(name string) (full string, skip bool, err error) {
	// 解压出来的路径一律按 / 分，Windows 的压缩包里有写反斜杠的，先统一。
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." || clean == "/" {
		return "", true, nil
	}
	// 绝对路径与任何形式的 .. 逃逸一律拒绝：这些东西马上要被放进安装目录，
	// 一份能往别处写文件的产物不能留。
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("产物里的路径跑到外面去了：%s", name)
	}
	base := path.Base(clean)
	// macOS 打包时顺手塞进来的元数据：__MACOSX/ 与 ._xxx 是资源分叉，
	// .DS_Store 是访达的目录显示信息，都不是产物的一部分。
	if clean == "__MACOSX" || strings.HasPrefix(clean, "__MACOSX/") ||
		strings.HasPrefix(base, "._") || base == ".DS_Store" {
		return "", true, nil
	}
	full = filepath.Join(u.root, filepath.FromSlash(clean))
	// 再核一遍落点确实在 dst 里面。上面那几条已经挡住了大部分写法，
	// 这一条挡的是各平台路径语义的边角（Windows 上的盘符名条目就是从这里漏出去的）。
	rel, err := filepath.Rel(u.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("产物里的路径跑到外面去了：%s", name)
	}
	return full, false, nil
}

// count 记一个条目并核上限。
func (u *unpacker) count() error {
	u.files++
	if u.files > maxExtractFiles {
		return errors.New("产物里的文件太多，不像是我们的包")
	}
	return nil
}

// file 落一个普通文件，返回它的大小。
func (u *unpacker) file(full string, mode os.FileMode, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, fmt.Errorf("创建目录失败：%w", err)
	}
	// 权限位掩到 0777：产物里带着 setuid / setgid 位的话，解出来的东西
	// 会以一种谁都没想到的身份运行。压缩包里没写权限位（unix 属性是 0）时
	// 落成 0644——照 0 落下去会得到一个谁都读不了的文件。
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return 0, fmt.Errorf("写出文件失败：%w", err)
	}
	defer f.Close()
	// 多读一个字节：正好读满上限时也要能发现「还有下文」。
	n, err := io.Copy(f, io.LimitReader(r, maxExtractBytes-u.bytes+1))
	u.bytes += n
	if u.bytes > maxExtractBytes {
		return n, errors.New("产物解出来太大，不像是我们的包")
	}
	if err != nil {
		return n, fmt.Errorf("写出文件失败：%w", err)
	}
	return n, nil
}

// dir 建一层目录。压缩包里的目录项只带自己的权限，这里统一按 0755 落。
func (u *unpacker) dir(full string) error {
	if err := os.MkdirAll(full, 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}
	return nil
}

func extractZip(archive, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开压缩包失败：%w", err)
	}
	defer zr.Close()

	u := &unpacker{root: dst}
	for _, f := range zr.File {
		full, skip, err := u.path(f.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := u.count(); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := u.dir(full); err != nil {
				return err
			}
			continue
		}
		// 软链与硬链一概不收：我们自己打出来的包里没有，而一条指向外面的链
		// 能让后面写进去的东西落到别处去。
		if f.Mode()&os.ModeSymlink != 0 || f.Mode()&os.ModeIrregular != 0 {
			return fmt.Errorf("产物里有不是普通文件的东西：%s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("读压缩包失败：%w", err)
		}
		_, err = u.file(full, f.Mode(), rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("打开压缩包失败：%w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("打开压缩包失败：%w", err)
	}
	defer gz.Close()

	u := &unpacker{root: dst}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读压缩包失败：%w", err)
		}
		full, skip, err := u.path(h.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := u.count(); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := u.dir(full); err != nil {
				return err
			}
		case tar.TypeReg:
			if _, err := u.file(full, os.FileMode(h.Mode), tr); err != nil {
				return err
			}
		default:
			// 软链、硬链、设备节点都不收，理由同上；tar 的 pax 与长文件名
			// 是 archive/tar 自己在 Next 里消化掉的，不会走到这儿来。
			return fmt.Errorf("产物里有不是普通文件的东西：%s", h.Name)
		}
	}
	return nil
}
