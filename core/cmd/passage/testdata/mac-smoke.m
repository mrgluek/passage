// Appended to the native bridge by scripts/test-mac.sh.
static int smokeAttempts;
static void smokePoll(void) {
    if (++smokeAttempts > 100) {
        fprintf(stderr, "SMOKE FAILED: timed out\n");
        exit(2);
    }
    if (passage.webview) {
        [passage.webview evaluateJavaScript:@"window.passageSmokeResult || ''" completionHandler:^(id value, NSError *error) {
            if ([value isKindOfClass:NSString.class] && [value length] > 0) {
                if (![value isEqualToString:@"ok"]) {
                    fprintf(stderr, "SMOKE FAILED: %s\n", [value UTF8String]);
                    exit(2);
                }
                NSWindow *original = passage.window;
                WKWebView *view = passage.webview;
                [original performClose:nil];
                if (original.visible) { fprintf(stderr, "SMOKE FAILED: close\n"); exit(2); }
                passageShowWindow("");
                dispatch_async(dispatch_get_main_queue(), ^{
                    if (passage.window != original || passage.webview != view || !original.visible) {
                        fprintf(stderr, "SMOKE FAILED: reuse\n"); exit(2);
                    }
                    [view takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *image, NSError *error) {
                        if (!image || error) { fprintf(stderr, "SMOKE FAILED: snapshot\n"); exit(2); }
                        NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithData:image.TIFFRepresentation];
                        NSString *path = NSProcessInfo.processInfo.environment[@"PASSAGE_SMOKE_IMAGE"];
                        if (path) {
                            [[bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}]
                                writeToFile:path atomically:YES];
                        }
                        fprintf(stderr, "SMOKE PASSED: init, view, settings, licenses, error reply, close/reopen, render\n");
                        passageQuit();
                    }];
                });
            } else {
                dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ smokePoll(); });
            }
        }];
    } else {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ smokePoll(); });
    }
}
__attribute__((constructor)) static void smokeStart(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ smokePoll(); });
}
