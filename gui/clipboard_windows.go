//go:build windows

package main

/*
#include <windows.h>
#include <stdlib.h>

// pierSetClipboard 把一段 UTF-8 文字放进系统剪贴板。
//
// 这一段落在 C 里，是为了 GlobalLock 那一句：它返回的是一个地址（uintptr），
// Go 那边要往这块内存里写字就得把它转回 unsafe.Pointer——那正是 go vet 的
// unsafeptr 检查会拦下的形状。拦得有道理（uintptr 不被 GC 跟踪），只是这一块
// 内存本来就不归 Go 管，转是安全的；与其在 Go 里写一行得靠人肉判断的豁免，
// 不如把整段挪进 C：调 Win32 本来就是 C 该在的地方。
//
// 全局内存的所有权在 SetClipboardData 成功之后归系统，失败时仍归我们，要还回去。
static void pierSetClipboard(const char *utf8) {
	if (utf8 == NULL) {
		return;
	}
	int wlen = MultiByteToWideChar(CP_UTF8, 0, utf8, -1, NULL, 0);
	if (wlen <= 0) {
		return;
	}
	HGLOBAL h = GlobalAlloc(GMEM_MOVEABLE, (SIZE_T)wlen * sizeof(WCHAR));
	if (h == NULL) {
		return;
	}
	LPWSTR dst = (LPWSTR)GlobalLock(h);
	if (dst == NULL) {
		GlobalFree(h);
		return;
	}
	if (MultiByteToWideChar(CP_UTF8, 0, utf8, -1, dst, wlen) <= 0) {
		GlobalUnlock(h);
		GlobalFree(h);
		return;
	}
	GlobalUnlock(h);

	if (!OpenClipboard(NULL)) {
		GlobalFree(h);
		return;
	}
	if (!EmptyClipboard() || SetClipboardData(CF_UNICODETEXT, h) == NULL) {
		// 没塞进去的时候这块内存还归我们，得还回去；塞进去了就归系统管。
		CloseClipboard();
		GlobalFree(h);
		return;
	}
	CloseClipboard();
}
*/
import "C"

import "unsafe"

// copyText 把一段文字放进系统剪贴板。
//
// 不引任何外部命令，也不走网页那一侧的 navigator.clipboard（那份页面是 SetHtml
// 喂进来的，origin 不是安全上下文，那个 API 会当场失败）。写剪贴板本来就是宿主的事。
func copyText(text string) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	C.pierSetClipboard(c)
}
