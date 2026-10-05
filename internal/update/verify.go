package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// checksum 找出这份产物该有的 sha256。
//
// 两条路：GitHub 的响应里每个资产自带 digest（sha256:<hex>），那是首选，不用多下
// 一个文件；没有就下发布里那份 SHA256SUMS。两条都不通就报错——没有校验和的更新
// 不做，宁可这一版升不了。
func (c *Client) checksum(ctx context.Context, r Release, a Asset) (string, error) {
	if hex, ok := strings.CutPrefix(a.Digest, "sha256:"); ok {
		hex = strings.ToLower(strings.TrimSpace(hex))
		if isHex64(hex) {
			return hex, nil
		}
	}
	sums, ok := r.Find(sumsName)
	if !ok {
		return "", errors.New(r.displayVersion() + "没有带校验和，这次更新不做")
	}
	data, err := c.getSmall(ctx, sums.URL)
	if err != nil {
		return "", fmt.Errorf("取 %s 失败：%w", sumsName, err)
	}
	sum, ok := parseSums(data, a.Name)
	if !ok {
		return "", fmt.Errorf("%s 里没有 %s 这一项", sumsName, a.Name)
	}
	return sum, nil
}

// getSmall 取一份小文件到内存（校验和文件只有几百字节）。
// 加个上限：基址是可以被顶掉的，别人塞回来一份几个 G 的东西就不好了。
func (c *Client) getSmall(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSumsBytes))
	if err != nil {
		return nil, err
	}
	if len(data) >= maxSumsBytes {
		return nil, errors.New("大得不像校验和文件")
	}
	return data, nil
}

// parseSums 从 SHA256SUMS 里挑出 name 那一行，返回它的 sha256。
//
// 每行是「<64 位十六进制><空白>./<文件名>」——package.sh 是 shasum ./*.zip 生成的，
// 路径带 ./ 前缀，所以按 basename 比而不是整串。二进制模式的行在路径前多一个 *。
func parseSums(data []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// 严格要两段：文件名里带空格的行解析不了，但我们发出去的名字里没有空格，
		// 与其猜，不如跳过。
		if len(fields) != 2 {
			continue
		}
		sum := strings.ToLower(fields[0])
		if !isHex64(sum) {
			continue
		}
		if filepath.Base(strings.TrimPrefix(fields[1], "*")) == name {
			return sum, true
		}
	}
	return "", false
}

// verifyFile 算一遍文件的 sha256 跟 want 比。
func verifyFile(path, want string) error {
	got, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("校验和不符（下回来的文件是坏的，已删掉）：想要 %s，实际 %s",
			abbrev(want), abbrev(got))
	}
	return nil
}

// fileMatches 报告 path 是不是就是想要的那一份：长度对得上（size 为 0 表示
// 服务端没说，那就只比内容）且内容验得过。
func fileMatches(path string, size int64, want string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if size > 0 && fi.Size() != size {
		return false, nil
	}
	return verifyFile(path, want) == nil, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读下载好的文件失败：%w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读下载好的文件失败：%w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// abbrev 把 64 位的校验和掐短，报错时只留头尾：整串写出来没人会去比，
// 写短了才看得出「这根本是两个不同的文件」。
func abbrev(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:8] + "…" + s[len(s)-8:]
}
