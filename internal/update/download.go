package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Progress 是下载进度回调，每读一块调一次。got 是文件当前落了多长（续传时从断点
// 起算，所以是「这个文件现在多大」，不是「这一趟下回来多少」），total 是预期总长，
// 为 0 表示服务端没说。
type Progress func(got, total int64)

const (
	// partSuffix 是没下完的那份的后缀。留着它是为了下次续传——
	// 本机网络实测会在大传输中途掐连接，没有续传就得让用户从头再点一次。
	partSuffix = ".part"

	// maxAttempts 是一次下载最多试几遍（含头一遍）。
	maxAttempts = 5

	// attemptTimeout 是单次尝试的时限，给得很松：几十兆在 200 KB/s 的线路上要几分钟，
	// 它挡的是「连接还在，但一个字节都不来」，不是慢。
	attemptTimeout = 10 * time.Minute

	// maxBackoff 是两次尝试之间的最长间隔。
	maxBackoff = 8 * time.Second

	// copyBuf 是读写的块大小。
	copyBuf = 128 << 10
)

// Download 把产物下到本地，返回落地路径。下载完就地校验：不通过就把那份字节删掉
// 再报错——绝不把没验过的东西交出去。
//
// 断点续传写的是 <名字>.part。只有服务端回了 206 且 Content-Range 的起点对得上
// 才接着往后写；回了 200（服务端不支持 Range，或者断点已经作废）就从零重写，
// 否则会拼出一份谁也认不出来的文件。
func (c *Client) Download(ctx context.Context, r Release, a Asset, onProgress Progress) (string, error) {
	if a.Name == "" || a.URL == "" {
		return "", errors.New("这一版里没有可下载的产物")
	}
	// 先拿到校验和再动手：没有校验和就根本不下，「先信一次」不在选项里。
	want, err := c.checksum(ctx, r, a)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", fmt.Errorf("创建下载目录失败：%w", err)
	}
	// a.Name 来自网上，落到本地前先削成一个纯文件名：产物包里带一个带路径的名字
	// 就会写到目录外面去。
	dest := filepath.Join(c.dir, filepath.Base(a.Name))
	part := dest + partSuffix

	// 上次已经下好并验过的直接用：几十兆不该为一份就躺在磁盘上的文件再下一次。
	if ok, err := fileMatches(dest, a.Size, want); err == nil && ok {
		return dest, nil
	}
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	if err := c.checkSpace(dest, a.Size-have); err != nil {
		return "", err
	}

	for attempt := 1; ; attempt++ {
		err = c.fetch(ctx, a.URL, part, a.Size, onProgress)
		if err == nil {
			break
		}
		// 再试也是这个结果的（404 这类）当场报出去，不耗掉剩下的机会。
		var perm permanent
		if errors.As(err, &perm) {
			return "", perm.err
		}
		// 取消与超时也不再试：那是用户按的，这条路本来就不通。
		if ctx.Err() != nil || attempt >= maxAttempts {
			return "", err
		}
		if werr := wait(ctx, backoff(attempt)); werr != nil {
			return "", err
		}
	}

	if err := verifyFile(part, want); err != nil {
		// 坏文件必须删掉：留着的话下次续传会从坏字节接着往后拼。
		os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dest); err != nil {
		return "", fmt.Errorf("保存下载结果失败：%w", err)
	}
	return dest, nil
}

// fetch 下（或者接着下）一份产物到 path。
//
// HTTP 状态这一类被包成 permanent——重试没有意义；网络中断原样返回，交给外面重试。
func (c *Client) fetch(ctx context.Context, url, path string, total int64, onProgress Progress) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	var have int64
	if fi, err := os.Stat(path); err == nil {
		have = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return permanent{err}
	}
	req.Header.Set("User-Agent", c.ua)
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("下载中断：%w", err)
	}
	defer resp.Body.Close()

	// 想续传得服务端明确答应：回 206，并且把起点写在 Content-Range 里。
	// 起点对不上（比如中途被换成了另一份文件）就当没续过，从零来。
	resume := false
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); ok && start == have {
			resume = true
		} else {
			have = 0
		}
	case http.StatusOK:
		have = 0
	default:
		return permanent{fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if resume {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return fmt.Errorf("写下载文件失败：%w", err)
	}
	defer f.Close()

	if total <= 0 {
		total = resp.ContentLength + have
	}
	if onProgress != nil {
		onProgress(have, total)
	}
	buf := make([]byte, copyBuf)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写下载文件失败：%w", werr)
			}
			have += int64(n)
			if onProgress != nil {
				onProgress(have, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("下载中断：%w", rerr)
		}
	}
	// 知道总长就核一下：少了说明这一趟没传完（连接被掐断），
	// 留着 .part 让外面重试时接着下。
	if total > 0 && have < total {
		return fmt.Errorf("下载中断：只收到 %d / %d 字节", have, total)
	}
	return nil
}

// permanent 包一个「再试也是这个结果」的错误：HTTP 404/403 这类重试没有意义，
// 直接报出去，不耗掉五次机会。
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// contentRangeStart 从 Content-Range（形如 "bytes 1234-5678/9999"）里取出起点。
func contentRangeStart(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	dash := strings.IndexByte(rest, '-')
	if dash < 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(rest[:dash], 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// backoff 是第 attempt 次失败之后等多久再试：1s、2s、4s、8s，封顶 8s。
func backoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// wait 等一段时间，中途被取消就立刻返回。
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// checkSpace 在动手之前先看一眼磁盘：装不下就当场说清楚，不要下到一半才失败。
//
// 要的不只是这一份产物：解压出来的那份和换文件时暂时留下的旧的都在同一个卷上，
// 所以按「两份产物再加一点余量」估。
func (c *Client) checkSpace(dest string, need int64) error {
	if need <= 0 {
		return nil
	}
	free, err := freeSpace(filepath.Dir(dest))
	if err != nil {
		// 问不出来就别拦着：这只是提前预警，真写不下时写文件那一步也会报错。
		return nil
	}
	want := need*2 + spaceSlack
	if free < want {
		return fmt.Errorf("磁盘空间不够：还要约 %d MB，%s 所在的分区只剩 %d MB",
			want>>20, filepath.Dir(dest), free>>20)
	}
	return nil
}

// spaceSlack 是磁盘检查留的余量：临时文件、解压时的目录项都要地方。
const spaceSlack = 64 << 20
