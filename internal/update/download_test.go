package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func sumOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// requestRangeStart 解请求里的 Range（"bytes=700-"）。它和响应里的
// Content-Range 是两种写法，别拿被测代码那个解析器去读它。
func requestRangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(v, "bytes=")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(rest, "-"), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// assetFor 拼一份指了指某个地址的产物。
func assetFor(name, url string, data []byte, digest bool) Asset {
	a := Asset{Name: name, URL: url, Size: int64(len(data))}
	if digest {
		a.Digest = "sha256:" + sumOf(data)
	}
	return a
}

func TestDownloadHappy(t *testing.T) {
	data := bytes.Repeat([]byte("pier"), 4096)
	var hits int32
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(data)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("Pier-0.3.0-macos-universal.zip", srv.URL+"/a.zip", data, true)
	rel := Release{Version: "0.3.0", Assets: []Asset{a}}

	var got, total int64
	path, err := c.Download(context.Background(), rel, a, func(g, tt int64) { got, total = g, tt })
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(c.Dir(), a.Name) {
		t.Errorf("落地在 %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Error("下回来的内容不对")
	}
	if got != int64(len(data)) || total != int64(len(data)) {
		t.Errorf("进度停在 %d / %d", got, total)
	}
	// .part 是下到一半的东西，成了就该改名，不该留着一个一模一样的副本。
	if _, err := os.Stat(path + partSuffix); !os.IsNotExist(err) {
		t.Error(".part 该被改名掉")
	}

	// 再下一次不该再走网络：几十兆不该为一份就躺在磁盘上的文件重下一遍。
	if _, err := c.Download(context.Background(), rel, a, nil); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("第二遍又发了 %d 次请求", n-1)
	}
}

// 断点续传：头一趟被掐断（本机网络实测就是这样），第二趟要带着 Range 从断点接着下，
// 拼出来的必须是完整的那一份。
func TestDownloadResumes(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 128)
	const cut = 700
	var ranges []string
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		if len(ranges) == 1 {
			// 声明整份长度，只发一截，然后把连接掐掉。
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack 失败：%v", err)
				return
			}
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(data))
			buf.Write(data[:cut])
			buf.Flush()
			conn.Close()
			return
		}
		start, ok := requestRangeStart(r.Header.Get("Range"))
		if !ok {
			t.Errorf("第二趟没带 Range，带的是 %q", r.Header.Get("Range"))
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start:])
	})

	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("Pier-0.3.0-macos-universal.tar.gz", srv.URL+"/a.tar.gz", data, true)

	path, err := c.Download(context.Background(), Release{Assets: []Asset{a}}, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 {
		t.Fatalf("发了 %d 次请求，想要 2 次", len(ranges))
	}
	if ranges[0] != "" {
		t.Errorf("头一趟不该带 Range，带了 %q", ranges[0])
	}
	if want := fmt.Sprintf("bytes=%d-", cut); ranges[1] != want {
		t.Errorf("第二趟的 Range 是 %q，想要 %q", ranges[1], want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("拼出来的文件是 %d 字节，想要 %d", len(body), len(data))
	}
}

// 服务端不认 Range（回 200 全量）时，必须把 .part 截断重写。
// 接着往后追加的话，拼出来的是一份头部是上次那半截、后面接一整份的坏文件——
// 大小还正好对得上，最后只能靠校验和才发现。
func TestFetchTruncatesWhenServerIgnoresRange(t *testing.T) {
	data := bytes.Repeat([]byte("pier"), 64)
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", data, true)
	part := filepath.Join(c.Dir(), a.Name+partSuffix)
	if err := os.WriteFile(part, []byte("上一次没下完的那半截"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.fetch(context.Background(), a.URL, part, a.Size, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("文件是 %d 字节，想要 %d", len(body), len(data))
	}
}

// Content-Range 的起点对不上（断点已经作废）时也走「从零重来」，不接着写。
func TestFetchRejectsMismatchedRange(t *testing.T) {
	data := bytes.Repeat([]byte("pier"), 64)
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 7-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[7:])
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", data, true)
	part := filepath.Join(c.Dir(), a.Name+partSuffix)
	if err := os.WriteFile(part, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 起点对不上，服务端又只给了后半截，这一趟注定凑不齐：该报「下载中断」
	// 而不是把后半截接到原来那 10 个字节后面。
	err := c.fetch(context.Background(), a.URL, part, int64(len(data)), nil)
	if err == nil || !strings.Contains(err.Error(), "下载中断") {
		t.Fatalf("该报下载中断，得到 %v", err)
	}
}

// 校验不过就删掉那份字节，绝不交出没验过的东西。
func TestDownloadRejectsBadChecksum(t *testing.T) {
	good := bytes.Repeat([]byte("pier"), 128)
	bad := []byte("这不是我们要的那份文件")
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bad)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	// 长度对得上（免得先撞上「没下完」），但内容是另一份：校验和该拦住它。
	a := Asset{Name: "a.zip", URL: srv.URL + "/a.zip", Size: int64(len(bad)), Digest: "sha256:" + sumOf(good)}

	_, err := c.Download(context.Background(), Release{Assets: []Asset{a}}, a, nil)
	if err == nil || !strings.Contains(err.Error(), "校验和不符") {
		t.Fatalf("该报校验和不符，得到 %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), a.Name)); !os.IsNotExist(err) {
		t.Error("验不过的文件不该留在原地")
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), a.Name+partSuffix)); !os.IsNotExist(err) {
		t.Error(".part 该删掉：留着的话下次续传会从坏字节接着拼")
	}
}

// 没有校验和就根本不下：连产物的地址都不该碰。
func TestDownloadRefusesWithoutChecksum(t *testing.T) {
	var hits int32
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("随便什么"))
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", []byte("随便什么"), false)

	_, err := c.Download(context.Background(), Release{Version: "0.3.0", Assets: []Asset{a}}, a, nil)
	if err == nil || !strings.Contains(err.Error(), "没有带校验和") {
		t.Fatalf("该说没有校验和，得到 %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("没有校验和还发了 %d 次请求", n)
	}
}

// SHA256SUMS 里那几行是「<校验和>  ./<文件名>」（package.sh 是 shasum ./*.zip
// 生成的），按整串比就一条都对不上。
func TestDownloadFallsBackToSums(t *testing.T) {
	data := bytes.Repeat([]byte("pier"), 256)
	var hits int32
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			fmt.Fprintf(w, "%s  ./a.zip\n%s  ./别的.zip\n", sumOf(data), strings.Repeat("0", 64))
			return
		}
		w.Write(data)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", data, false)
	rel := Release{Version: "0.3.0", Assets: []Asset{
		a,
		{Name: sumsName, URL: srv.URL + "/SHA256SUMS"},
	}}

	if _, err := c.Download(context.Background(), rel, a, nil); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("发了 %d 次请求，想要 2 次（校验和 + 产物）", n)
	}
}

// 产物地址回 404 是「再试也是这个结果」，不该耗掉五次机会。
func TestDownloadDoesNotRetryPermanentFailures(t *testing.T) {
	var hits int32
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", []byte("x"), true)

	_, err := c.Download(context.Background(), Release{Assets: []Asset{a}}, a, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("该报 404，得到 %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("404 试了 %d 次", n)
	}
}

// 取消要立刻返回，不能等重试那一圈走完。
func TestDownloadStopsOnCancel(t *testing.T) {
	srv := githubStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTP: srv.Client()})
	a := assetFor("a.zip", srv.URL+"/a.zip", []byte("x"), true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Download(ctx, Release{Assets: []Asset{a}}, a, nil); err == nil {
		t.Fatal("取消了还继续下")
	}
}

// 磁盘装不下就当场说清楚，不要下到一半才失败。
func TestCheckSpaceSaysItPlainly(t *testing.T) {
	c := newTestClient(t, Options{})
	err := c.checkSpace(filepath.Join(c.Dir(), "a.zip"), 1<<50)
	if err == nil || !strings.Contains(err.Error(), "磁盘空间不够") {
		t.Fatalf("该说磁盘空间不够，得到 %v", err)
	}
	if err := c.checkSpace(filepath.Join(c.Dir(), "a.zip"), 1); err != nil {
		t.Errorf("一点点空间也要得不多，不该拦：%v", err)
	}
	if err := c.checkSpace(filepath.Join(c.Dir(), "a.zip"), 0); err != nil {
		t.Errorf("不需要新空间时不该拦：%v", err)
	}
}

func TestContentRangeStart(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"bytes 100-199/200", 100, true},
		{"bytes 0-0/1", 0, true},
		{"bytes */200", 0, false},
		{"", 0, false},
		{"100-199/200", 0, false},
		{"bytes abc-199/200", 0, false},
		{"bytes -199/200", 0, false},
	}
	for _, c := range cases {
		got, ok := contentRangeStart(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("contentRangeStart(%q) = %d, %v", c.in, got, c.ok)
		}
	}
}

func TestBackoffCaps(t *testing.T) {
	want := []int{1, 2, 4, 8, 8}
	for i, secs := range want {
		if got := backoff(i + 1); int(got.Seconds()) != secs {
			t.Errorf("backoff(%d) = %v，想要 %ds", i+1, got, secs)
		}
	}
}
