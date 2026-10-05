package diag

import (
	"io"
	"os"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// LogTailBytes 是读日志时最多看的字节数。
//
// 只读尾部：界面每两秒快照一次，出事的那几个服务每次都要认一遍，整读一个几百兆的
// 日志是拿不住的。64 KB 对「认出最后一行为什么失败」也够了——真凶就在最后一次运行的
// 末尾，上面那些是它一路走来的过程。
const LogTailBytes = 64 << 10

// FromLog 读服务最后一次运行的日志尾部，认一条出来。
//
// 认不出来（还没跑过、日志被清过、错误不在这一层）时返回 false，
// 调用方据此什么都不显示——空着比说错强。
func FromLog(cfg *config.Config, name string) (Hit, bool) {
	return Scan(Tail(cfg, name))
}

// Tail 返回服务最后一次运行的日志尾部。
//
// 「最后一次运行」由 proc.TrimToLastRun 保证：同一天的多次启动追加在同一份文件里，
// 不清掉上面那些的话，上一次运行留下的 EADDRINUSE 会被当成这一次的原因报出来——
// 而这一次可能只是编译慢了点。
func Tail(cfg *config.Config, name string) string {
	f, err := os.Open(proc.LogFile(cfg, name))
	if err != nil {
		return ""
	}
	defer f.Close()
	return proc.TrimToLastRun(readTail(f, LogTailBytes), name)
}

// readTail 读文件末尾最多 n 字节。
//
// 从中间开始读时，第一行多半是半截的——中文还会在 UTF-8 的字节中间断开，
// 显示出来是一片乱码，而这半行是要抄给用户看的，所以宁可整行走掉。
func readTail(f *os.File, n int64) string {
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	start := int64(0)
	if size > n {
		start = size - n
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return ""
	}
	text := string(raw)
	if start > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			// 这一整块里就一行，还是半截的，没有可用内容。
			return ""
		}
		text = text[i+1:]
	}
	return text
}
