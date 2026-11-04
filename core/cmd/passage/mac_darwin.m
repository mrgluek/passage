//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include "mac_darwin.h"

@interface PassageWindow : NSObject <WKScriptMessageHandler, WKNavigationDelegate, NSWindowDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *webview;
@end

@implementation PassageWindow
- (void)quit:(id)sender {
    passageQuit();
}

- (void)userContentController:(WKUserContentController *)controller
      didReceiveScriptMessage:(WKScriptMessage *)message {
    if (!message.frameInfo.mainFrame || ![message.body isKindOfClass:[NSDictionary class]]) return;
    NSData *data = [NSJSONSerialization dataWithJSONObject:message.body options:0 error:nil];
    if (data) {
        NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
        passageCall((char *)json.UTF8String);
    }
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
       decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
    // Only the initial bundled document can use the native bridge.
    BOOL initial = action.navigationType == WKNavigationTypeOther &&
        [action.request.URL.absoluteString isEqualToString:@"about:blank"];
    decisionHandler(initial ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
    [sender orderOut:nil];
    return NO;
}
@end

static PassageWindow *passage;

void passageShowWindow(const char *html) {
    NSString *document = [NSString stringWithUTF8String:html];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!passage) {
            passage = [PassageWindow new];
            NSMenu *menu = [NSMenu new];
            NSMenuItem *appItem = [NSMenuItem new];
            [menu addItem:appItem];
            appItem.submenu = [[NSMenu alloc] initWithTitle:@"Passage"];
            NSMenuItem *quit = [appItem.submenu addItemWithTitle:@"Quit Passage"
                action:@selector(quit:) keyEquivalent:@"q"];
            quit.target = passage;
            NSMenuItem *editItem = [NSMenuItem new];
            [menu addItem:editItem];
            editItem.submenu = [[NSMenu alloc] initWithTitle:@"Edit"];
            [editItem.submenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
            [editItem.submenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"Z"];
            [editItem.submenu addItem:[NSMenuItem separatorItem]];
            [editItem.submenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
            [editItem.submenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
            [editItem.submenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
            [editItem.submenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
            NSApp.mainMenu = menu;
            WKWebViewConfiguration *config = [WKWebViewConfiguration new];
            config.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
            [config.userContentController addScriptMessageHandler:passage name:@"passage"];
            NSString *bridge = @"window.passageCall = (id, method, arg) => "
                @"window.webkit.messageHandlers.passage.postMessage({id, method, arg});";
            [config.userContentController addUserScript:[[WKUserScript alloc]
                initWithSource:bridge injectionTime:WKUserScriptInjectionTimeAtDocumentStart
                forMainFrameOnly:YES]];
            passage.webview = [[WKWebView alloc] initWithFrame:NSZeroRect configuration:config];
            passage.webview.navigationDelegate = passage;
            passage.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 560, 780)
                styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                    NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                backing:NSBackingStoreBuffered defer:NO];
            passage.window.title = @"Passage";
            passage.window.minSize = NSMakeSize(400, 480);
            passage.window.releasedWhenClosed = NO;
            passage.window.delegate = passage;
            passage.window.contentView = passage.webview;
            [passage.window center];
            [passage.webview loadHTMLString:document baseURL:nil];
        }
        [passage.window deminiaturize:nil];
        [passage.window makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

void passageReply(const char *script) {
    NSString *source = [NSString stringWithUTF8String:script];
    dispatch_async(dispatch_get_main_queue(), ^{
        [passage.webview evaluateJavaScript:source completionHandler:nil];
    });
}

void passageError(const char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        NSAlert *alert = [NSAlert new];
        alert.messageText = @"Passage";
        alert.informativeText = [NSString stringWithUTF8String:message];
        [alert runModal];
    }
}

const char *passageLanguage(void) {
    return NSLocale.preferredLanguages.firstObject.UTF8String;
}
