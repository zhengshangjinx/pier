package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// package.sh 生成的那份是 `shasum -a 256 ./*.zip …`，路径带 ./ 前缀。
// 按整串比就一条都对不上，只能按 basename 比。
func TestParseSumsDotSlashPrefix(t *testing.T) {
	data := sumA + "  ./Pier-0.3.0-macos-universal.zip\n" +
		sumB + "  ./Pier-0.3.0-macos-universal.tar.gz\n"
	got, ok := parseSums([]byte(data), "Pier-0.3.0-macos-universal.tar.gz")
	if !ok || got != sumB {
		t.Fatalf("挑出来的是 %q, %v", got, ok)
	}
}

func TestParseSums(t *testing.T) {
	// 二进制模式那几行在路径前多一个 *（shasum -b 的写法）。
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"普通", sumA + "  a.zip\n", sumA, true},
		{"点斜杠", sumA + "  ./a.zip\n", sumA, true},
		{"二进制模式", sumA + "  *a.zip\n", sumA, true},
		{"路径带目录", sumA + "  dist/a.zip\n", sumA, true},
		{"大写十六进制", strings.ToUpper(sumA) + "  a.zip\n", sumA, true},
		{"前面有注释和空行", "# 校验和\n\n" + sumA + "  a.zip\n", sumA, true},
		{"最后一行没有换行", sumA + "  a.zip", sumA, true},
		{"夹在别的行中间", sumB + "  b.zip\n" + sumA + "  a.zip\n" + sumB + "  c.zip\n", sumA, true},
		{"没有这一项", sumA + "  b.zip\n", "", false},
		{"校验和位数不够", "aaaa  a.zip\n", "", false},
		{"校验和里有非十六进制", sumA[:63] + "z  a.zip\n", "", false},
		{"多了第三段", sumA + "  a.zip  多写的\n", "", false},
		{"空文件", "", "", false},
		// 我们发出去的名字里没有空格，带空格的行解析不了就跳过，不猜。
		{"文件名带空格", sumA + "  a b.zip\n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseSums([]byte(c.body), "a.zip")
			if ok != c.ok || got != c.want {
				t.Errorf("parseSums = %q, %v，想要 %q, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestIsHex64(t *testing.T) {
	if !isHex64(sumA) {
		t.Error("64 位小写十六进制该认")
	}
	if !isHex64(strings.ToUpper(sumA)) {
		t.Error("大写也该认")
	}
	if isHex64(sumA[:63]) || isHex64(sumA+"0") {
		t.Error("长度不对的不该认")
	}
	if isHex64(strings.Replace(sumA, "a", "g", 1)) {
		t.Error("非十六进制的字符不该认")
	}
	if isHex64("") {
		t.Error("空串不该认")
	}
}

func TestVerifyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.zip")
	body := []byte("这是我们下回来的那份")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(path, sumOf(body)); err != nil {
		t.Fatalf("对得上的该通过：%v", err)
	}
	// 大写也得认：SHA256SUMS 里是什么样就什么样，不该为大小写再纠一次。
	if err := verifyFile(path, strings.ToUpper(sumOf(body))); err != nil {
		t.Errorf("大写校验和该通过：%v", err)
	}

	wrong := sumOf([]byte("另一份"))
	err := verifyFile(path, wrong)
	if err == nil || !strings.Contains(err.Error(), "校验和不符") {
		t.Fatalf("该报校验和不符，得到 %v", err)
	}
	// 报错里要有头有尾的十六进制，不然看不出「这根本是两个不同的文件」。
	for _, want := range []string{abbrev(wrong), abbrev(sumOf(body))} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错里该带上 %s：%q", want, err)
		}
	}

	if err := verifyFile(filepath.Join(t.TempDir(), "不在"), sumA); err == nil {
		t.Error("文件不在该报错")
	}
}

// 已经下好的那份要认出来，不然每次点下载都要重来一遍几十兆。
func TestFileMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.zip")
	body := []byte("下好的")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := fileMatches(path, int64(len(body)), sumOf(body)); err != nil || !ok {
		t.Errorf("该认出这就是要的那一份：%v %v", ok, err)
	}
	// 服务端没说大小（0）时只比内容。
	if ok, err := fileMatches(path, 0, sumOf(body)); err != nil || !ok {
		t.Errorf("没给大小时该只比内容：%v %v", ok, err)
	}
	if ok, _ := fileMatches(path, int64(len(body))+1, sumOf(body)); ok {
		t.Error("大小对不上就不该认")
	}
	if ok, _ := fileMatches(path, int64(len(body)), sumOf([]byte("别的东西"))); ok {
		t.Error("内容对不上就不该认")
	}
	if ok, err := fileMatches(filepath.Join(t.TempDir(), "不在"), 0, sumA); err == nil || ok {
		t.Error("文件不在该报错")
	}
}

func TestAbbrev(t *testing.T) {
	if got := abbrev(sumA); got != "aaaaaaaa…aaaaaaaa" {
		t.Errorf("abbrev = %q", got)
	}
	if got := abbrev("abc"); got != "abc" {
		t.Errorf("短的该原样，得到 %q", got)
	}
}
