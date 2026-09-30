//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// 网页里的「选择文件」：窗口库自带的处理对象创建后即被释放（网页视图只弱引用它），这里换成常驻的实现
@interface CCUIDelegate : NSObject <WKUIDelegate>
@end

@implementation CCUIDelegate
- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *))completionHandler {
	NSOpenPanel *panel = [NSOpenPanel openPanel];
	panel.canChooseFiles = YES;
	panel.canChooseDirectories = parameters.allowsDirectories;
	panel.allowsMultipleSelection = parameters.allowsMultipleSelection;
	[panel beginSheetModalForWindow:webView.window completionHandler:^(NSModalResponse r) {
		completionHandler(r == NSModalResponseOK ? panel.URLs : nil);
	}];
}
@end

static CCUIDelegate *ccDelegate;

static NSMenuItem *ccItem(NSMenu *menu, NSString *title, SEL action, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *it = [menu addItemWithTitle:title action:action keyEquivalent:key];
	it.keyEquivalentModifierMask = mods;
	return it;
}

// ccSetup：接管网页视图的文件选择，并建立菜单栏（「编辑」菜单让 ⌘C、⌘V、⌘X、⌘A、⌘Z 在网页里生效）
static void ccSetup(void *win) {
	NSWindow *w = (__bridge NSWindow *)win;
	if ([w.contentView isKindOfClass:[WKWebView class]]) {
		if (!ccDelegate) ccDelegate = [CCUIDelegate new];
		((WKWebView *)w.contentView).UIDelegate = ccDelegate;
	}
	NSEventModifierFlags cmd = NSEventModifierFlagCommand;
	NSMenu *bar = [NSMenu new];
	// 应用菜单
	NSMenu *app = [NSMenu new];
	ccItem(app, @"隐藏 HomeGuard", @selector(hide:), @"h", cmd);
	ccItem(app, @"隐藏其他", @selector(hideOtherApplications:), @"h", cmd | NSEventModifierFlagOption);
	[app addItem:[NSMenuItem separatorItem]];
	ccItem(app, @"退出 HomeGuard", @selector(terminate:), @"q", cmd);
	[bar addItemWithTitle:@"" action:nil keyEquivalent:@""].submenu = app;
	// 编辑菜单
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
	ccItem(edit, @"撤销", @selector(undo:), @"z", cmd);
	ccItem(edit, @"重做", @selector(redo:), @"z", cmd | NSEventModifierFlagShift);
	[edit addItem:[NSMenuItem separatorItem]];
	ccItem(edit, @"剪切", @selector(cut:), @"x", cmd);
	ccItem(edit, @"复制", @selector(copy:), @"c", cmd);
	ccItem(edit, @"粘贴", @selector(paste:), @"v", cmd);
	ccItem(edit, @"粘贴并匹配样式", @selector(pasteAsPlainText:), @"v", cmd | NSEventModifierFlagOption | NSEventModifierFlagShift);
	ccItem(edit, @"全选", @selector(selectAll:), @"a", cmd);
	[bar addItemWithTitle:@"编辑" action:nil keyEquivalent:@""].submenu = edit;
	// 窗口菜单
	NSMenu *win2 = [[NSMenu alloc] initWithTitle:@"窗口"];
	ccItem(win2, @"最小化", @selector(performMiniaturize:), @"m", cmd);
	ccItem(win2, @"关闭窗口", @selector(performClose:), @"w", cmd);
	[bar addItemWithTitle:@"窗口" action:nil keyEquivalent:@""].submenu = win2;
	[NSApplication sharedApplication].mainMenu = bar;
}
*/
import "C"

import (
	"runtime"

	webview "github.com/webview/webview_go"
)

/** init：macOS 的窗口只能在主线程创建和运行，把主协程固定在主线程上 */
func init() { runtime.LockOSThread() }

/** Show：打开窗口并阻塞到窗口关闭 */
func Show(w Window) error {
	v := webview.New(false)
	defer v.Destroy()
	v.SetTitle(w.Title)
	v.SetSize(w.Width, w.Height, webview.HintNone)
	C.ccSetup(v.Window())
	v.Navigate(w.URL)
	v.Run()
	return nil
}
