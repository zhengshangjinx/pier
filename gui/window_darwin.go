//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#include <stdlib.h>

// 标题栏那一条的高度，与 app.css 里 .dc-native 留出的顶距一致。
#define PIER_TITLEBAR_HEIGHT 28.0

// PierDragStrip 是盖在网页顶上的一条透明拖拽区。
//
// 页面一直铺到窗口最顶（全尺寸内容视图），弹窗的遮罩才能连标题栏那一条一起盖住；
// 代价是那一条不再是系统标题栏，WKWebView 会把点击吃掉，窗口就拖不动了。
// 所以在最上层垫一条原生视图接管拖拽和双击缩放。它只占标题栏的高度，
// 页面在这一条里不放任何可点的东西（见 app.css 的 .dc-native）。
@interface PierDragStrip : NSView
@end

@implementation PierDragStrip
- (BOOL)mouseDownCanMoveWindow { return YES; }
- (BOOL)acceptsFirstMouse:(NSEvent *)event { return YES; }
- (void)mouseDown:(NSEvent *)event {
	if (event.clickCount == 2) {
		// 跟随系统设置里「双击窗口标题栏」的选择：缩放或最小化。
		NSString *action = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleActionOnDoubleClick"];
		if ([action isEqualToString:@"Minimize"]) {
			[self.window performMiniaturize:nil];
		} else if (![action isEqualToString:@"None"]) {
			[self.window performZoom:nil];
		}
		return;
	}
	[self.window performWindowDragWithEvent:event];
}
@end

// 标题栏透明、不写标题、去掉分隔线，内容铺满整个窗口。
//
// 单拎出来是因为它会被抹掉：webview 库的 SetSize 用一份写死的掩码调 setStyleMask:
// （webview.h 的 set_size_impl），而那是整个换掉掩码，不是往里加一位。
// 抹掉之后内容视图退回标题栏下面，顶上 28px 又归系统标题栏（底色是窗口背景色），
// 弹窗的遮罩是 fixed 定位、盖的是内容视图，于是那一条亮着盖不住——就是界面上
// 那一条。所以定尺寸之后必须补一次，见 Go 侧的 resize。
static void pierFullSizeContentView(void *p) {
	NSWindow *w = (NSWindow *)p;
	// 换掩码时 AppKit 保的是内容区的大小，于是外框会平白缩掉标题栏那一条：
	// 尺寸是调用方定好的，不该因为样式而变，所以先记下来、换完再照原样定回去。
	NSRect f = w.frame;
	w.titlebarAppearsTransparent = YES;
	w.titleVisibility = NSWindowTitleHidden;
	w.styleMask |= NSWindowStyleMaskFullSizeContentView;
	if (@available(macOS 11.0, *)) {
		w.titlebarSeparatorStyle = NSTitlebarSeparatorStyleNone;
	}
	if (!NSEqualRects(f, w.frame)) {
		[w setFrame:f display:YES];
	}
}

// pierInstallDragStrip 在最上面垫一条原生拖拽区。重复调用只会留下一条。
static void pierInstallDragStrip(void *p) {
	NSWindow *w = (NSWindow *)p;
	NSView *host = w.contentView;

	NSArray *subs = host.subviews;
	for (NSInteger i = (NSInteger)subs.count - 1; i >= 0; i--) {
		NSView *v = [subs objectAtIndex:(NSUInteger)i];
		if ([v isKindOfClass:[PierDragStrip class]]) {
			[v removeFromSuperview];
		}
	}

	NSRect b = host.bounds;
	CGFloat h = PIER_TITLEBAR_HEIGHT;
	PierDragStrip *strip = [[PierDragStrip alloc] init];
	if (host.isFlipped) {
		strip.frame = NSMakeRect(0, 0, b.size.width, h);
		strip.autoresizingMask = NSViewWidthSizable | NSViewMaxYMargin;
	} else {
		strip.frame = NSMakeRect(0, b.size.height - h, b.size.width, h);
		strip.autoresizingMask = NSViewWidthSizable | NSViewMinYMargin;
	}
	[host addSubview:strip positioned:NSWindowAbove relativeTo:nil];
}

// pierInstallMenu 装上标准的应用菜单与「编辑」菜单。
//
// macOS 上 ⌘C / ⌘V / ⌘X / ⌘A / ⌘Z 不是网页自己处理的：按键先交给菜单栏找对应的
// 菜单项，菜单项再把 copy: / paste: 之类的动作沿响应链发给当前焦点（这里是 WKWebView）。
// webview 库不建菜单栏，于是这些快捷键在所有输入框里都没反应——这是最基本的体验，
// 必须补上。动作的目标留空（nil），让系统按响应链去找，输入框、日志区、弹窗里都一样生效。
static NSMenuItem *pierItem(NSString *title, SEL action, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
	it.keyEquivalentModifierMask = mods;
	return it;
}

static void pierInstallMenu(void) {
	NSEventModifierFlags cmd = NSEventModifierFlagCommand;
	NSMenu *bar = [[NSMenu alloc] init];

	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	NSMenu *app = [[NSMenu alloc] initWithTitle:@"Pier"];
	[app addItem:pierItem(@"隐藏 Pier", @selector(hide:), @"h", cmd)];
	[app addItem:pierItem(@"隐藏其他", @selector(hideOtherApplications:), @"h", cmd | NSEventModifierFlagOption)];
	[app addItem:pierItem(@"全部显示", @selector(unhideAllApplications:), @"", 0)];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:pierItem(@"退出 Pier", @selector(terminate:), @"q", cmd)];
	appItem.submenu = app;
	[bar addItem:appItem];

	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
	[edit addItem:pierItem(@"撤销", @selector(undo:), @"z", cmd)];
	[edit addItem:pierItem(@"重做", @selector(redo:), @"z", cmd | NSEventModifierFlagShift)];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:pierItem(@"剪切", @selector(cut:), @"x", cmd)];
	[edit addItem:pierItem(@"拷贝", @selector(copy:), @"c", cmd)];
	[edit addItem:pierItem(@"粘贴", @selector(paste:), @"v", cmd)];
	[edit addItem:pierItem(@"粘贴并匹配样式", @selector(pasteAsPlainText:), @"v", cmd | NSEventModifierFlagOption | NSEventModifierFlagShift)];
	[edit addItem:pierItem(@"删除", @selector(delete:), @"", 0)];
	[edit addItem:pierItem(@"全选", @selector(selectAll:), @"a", cmd)];
	editItem.submenu = edit;
	[bar addItem:editItem];

	NSMenuItem *winItem = [[NSMenuItem alloc] init];
	NSMenu *win = [[NSMenu alloc] initWithTitle:@"窗口"];
	[win addItem:pierItem(@"最小化", @selector(performMiniaturize:), @"m", cmd)];
	[win addItem:pierItem(@"缩放", @selector(performZoom:), @"", 0)];
	[win addItem:pierItem(@"关闭窗口", @selector(performClose:), @"w", cmd)];
	winItem.submenu = win;
	[bar addItem:winItem];
	[NSApp setWindowsMenu:win];

	[NSApp setMainMenu:bar];
}

// pierCopyText 把一段文字放进系统剪贴板。
//
// 不走网页那一侧的 navigator.clipboard：这份页面是 SetHtml 喂进来的，
// origin 不是安全上下文，那个 API 会当场失败（拿到一个永远不 resolve 的 promise），
// 界面上表现为「点了复制，什么都没发生、也没有报错」。写剪贴板本来就是宿主的事。
static void pierCopyText(const char *text) {
	if (text == NULL) {
		return;
	}
	NSString *s = [NSString stringWithUTF8String:text];
	if (s == nil) {
		return;
	}
	NSPasteboard *pb = [NSPasteboard generalPasteboard];
	[pb clearContents];
	[pb setString:s forType:NSPasteboardTypeString];
}

// pierSetBadge 在 Dock 图标上挂一个角标，空串清掉。
//
// 这是这个应用里唯一「人不看着它也看得见」的地方：标题栏按
// pierFullSizeContentView 是透明且不画标题的，窗口标题只有 Mission Control
// 和「窗口」菜单里读得到。所以「有几个服务要关注」只能落在 Dock 图标上。
static void pierSetBadge(const char *text) {
	NSString *s = (text == NULL) ? nil : [NSString stringWithUTF8String:text];
	if (s != nil && s.length == 0) {
		// 空串与 nil 在 AppKit 里都是「清掉」，但不替我们认空串：传 "" 会挂出
		// 一个空白的红色圆角块，比不清还难看。
		s = nil;
	}
	[[NSApp dockTile] setBadgeLabel:s];
}

// pierBounce 让 Dock 图标跳一下：窗口在后台时角标根本看不见，这一下才是叫人的那部分。
//
// 用 NSInformationalRequest 而不是 NSCriticalRequest——后者会一直跳到来点它为止，
// 一个跑挂了的本地服务不值当这个。
static void pierBounce(void) {
	[NSApp requestUserAttention:NSInformationalRequest];
}

// pierMainScreen 带菜单栏的那块屏。
static NSScreen *pierMainScreen(void) {
	NSArray *screens = [NSScreen screens];
	return screens.count > 0 ? [screens objectAtIndex:0] : [NSScreen mainScreen];
}

// pierScreenVisible 主屏的可用区域（已经扣掉菜单栏和 Dock），单位是点。
static void pierScreenVisible(double *out) {
	NSScreen *s = pierMainScreen();
	NSRect r = s ? s.visibleFrame : NSMakeRect(0, 0, 0, 0);
	out[0] = r.size.width;
	out[1] = r.size.height;
}

// pierCenterWindow 把窗口摆到主屏可用区域的正中。
//
// webview 的 SetSize 末尾会自己 center 一次（webview.h 的 set_size_impl），
// 但那时窗口还是「内容视图缩在标题栏下面」的样子，摆出来偏上；样式铺好之后再摆一次，
// 上下留的边才是匀的。
static void pierCenterWindow(void *p) {
	NSWindow *w = (NSWindow *)p;
	NSScreen *s = pierMainScreen();
	if (!s) {
		return;
	}
	NSRect vf = s.visibleFrame;
	NSRect f = w.frame;
	f.origin.x = vf.origin.x + (vf.size.width - f.size.width) / 2;
	f.origin.y = vf.origin.y + (vf.size.height - f.size.height) / 2;
	// 窗口比可用区域还大的时候别把它推到菜单栏底下去。
	if (f.origin.x < vf.origin.x) f.origin.x = vf.origin.x;
	if (f.origin.y < vf.origin.y) f.origin.y = vf.origin.y;
	[w setFrame:f display:YES];
}

// 底色跟着界面的主题走；外观（亮/暗）也要一起切，否则暗色下红黄绿三个按钮
// 和窗口阴影还是亮色那一套，一眼就能看出是两层东西。
static void pierSetChrome(void *p, double r, double g, double b, int dark) {
	NSWindow *w = (NSWindow *)p;
	w.backgroundColor = [NSColor colorWithSRGBRed:r green:g blue:b alpha:1];
	w.appearance = [NSAppearance appearanceNamed:(dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua)];
}
*/
import "C"

import (
	"unsafe"

	webview "github.com/webview/webview_go"
)

// nativeWindowChrome 为真表示页面铺到了标题栏底下，顶上那一条由原生代码接管
// （见 app.css 的 .dc-native）。
const nativeWindowChrome = true

// fileManagerName 是 macOS 上文件管理器的叫法，界面上的「在 ✕ 中显示」用它。
const fileManagerName = "访达"

// styleWindow 在窗口创建后、页面加载前调用一次：装上菜单栏、让页面铺满整个窗口、
// 垫回拖拽区，最后把窗口在屏幕可用区域里摆正。
// 必须在主线程上调用（main 里 webview.New 之后正是主线程），并且要在 resize 之后。
func styleWindow(win unsafe.Pointer) {
	C.pierInstallMenu()
	if win != nil {
		C.pierFullSizeContentView(win)
		C.pierInstallDragStrip(win)
		C.pierCenterWindow(win)
	}
}

// resize 定窗口尺寸。改尺寸一律走这里，别直接调 webview 的 SetSize：
// 它内部是用一份写死的掩码调 setStyleMask:，「全尺寸内容视图」那一位会被换掉，
// 于是窗口顶上又冒出系统标题栏那一条（见 pierFullSizeContentView）。
func resize(w webview.WebView, width, height int, hint webview.Hint) {
	w.SetSize(width, height, hint)
	if win := w.Window(); win != nil {
		C.pierFullSizeContentView(win)
	}
}

// screenVisible 主屏可用区域（扣掉菜单栏与 Dock），拿不到时返回 0。
func screenVisible() (float64, float64) {
	var out [2]C.double
	C.pierScreenVisible(&out[0])
	return float64(out[0]), float64(out[1])
}

// copyText 把一段文字放进系统剪贴板。
func copyText(text string) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	C.pierCopyText(c)
}

// setBadge 在 Dock 图标上挂角标，空串清掉。必须在主线程调用。
func setBadge(text string) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	C.pierSetBadge(c)
}

// bounceIcon 让 Dock 图标跳一下。必须在主线程调用。
func bounceIcon() {
	C.pierBounce()
}

// applyChrome 把窗口底色刷成页面底色。必须在主线程调用。
func applyChrome(win unsafe.Pointer, r, g, b float64, dark bool) {
	if win == nil {
		return
	}
	d := C.int(0)
	if dark {
		d = 1
	}
	C.pierSetChrome(win, C.double(r), C.double(g), C.double(b), d)
}
