// Appended to the native bridge by scripts/test-mac.sh.
static int smokeAttempts;
static NSTask *smokeSecond;

static void smokeSendURL(void) {
    NSAppleEventDescriptor *target = [NSAppleEventDescriptor descriptorWithProcessIdentifier:NSProcessInfo.processInfo.processIdentifier];
    NSAppleEventDescriptor *event = [NSAppleEventDescriptor appleEventWithEventClass:kInternetEventClass
        eventID:kAEGetURL targetDescriptor:target returnID:kAutoGenerateReturnID transactionID:kAnyTransactionID];
    [event setParamDescriptor:[NSAppleEventDescriptor descriptorWithString:NSProcessInfo.processInfo.environment[@"PASSAGE_SMOKE_LINK"]]
        forKeyword:keyDirectObject];
    NSError *error = nil;
    [event sendEventWithOptions:NSAppleEventSendNoReply timeout:1 error:&error];
    if (error) { fprintf(stderr, "SMOKE FAILED: URL event\n"); exit(2); }
}

static void smokeReopened(NSWindow *original, WKWebView *view, BOOL secondProcess) {
    if (++smokeAttempts > 100) {
        fprintf(stderr, "SMOKE FAILED: reopen timed out\n");
        exit(2);
    }
    if (!original.visible || (secondProcess && smokeSecond.running)) {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
            smokeReopened(original, view, secondProcess);
        });
        return;
    }
    if (passage.window != original || passage.webview != view ||
        (secondProcess && smokeSecond.terminationStatus != 0)) {
        fprintf(stderr, "SMOKE FAILED: window reuse or second process\n");
        exit(2);
    }
    if (!secondProcess) {
        [original performClose:nil];
        smokeSecond = [NSTask new];
        NSArray<NSString *> *args = NSProcessInfo.processInfo.arguments;
        smokeSecond.executableURL = [NSURL fileURLWithPath:args.firstObject];
        smokeSecond.arguments = [args subarrayWithRange:NSMakeRange(1, args.count - 1)];
        NSError *error = nil;
        if (![smokeSecond launchAndReturnError:&error]) {
            fprintf(stderr, "SMOKE FAILED: second process: %s\n", error.description.UTF8String);
            exit(2);
        }
        smokeAttempts = 0;
        smokeReopened(original, view, YES);
        return;
    }
    [view takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *image, NSError *error) {
        if (!image || error) { fprintf(stderr, "SMOKE FAILED: snapshot\n"); exit(2); }
        NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithData:image.TIFFRepresentation];
        NSString *path = NSProcessInfo.processInfo.environment[@"PASSAGE_SMOKE_IMAGE"];
        if (path) {
            [[bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}]
                writeToFile:path atomically:YES];
        }
        fprintf(stderr, "SMOKE PASSED: bridge, URL import/cancel, Finder reopen, second process, window reuse, render\n");
        passageQuit();
    }];
}

static void smokePoll(void) {
    if (++smokeAttempts > 100) {
        fprintf(stderr, "SMOKE FAILED: timed out\n");
        exit(2);
    }
    if (passage.webview) {
        [passage.webview evaluateJavaScript:@"window.passageSmokeResult || ''" completionHandler:^(id value, NSError *error) {
            if ([value isEqual:@"send-url"]) {
                [passage.webview evaluateJavaScript:@"window.passageSmokeResult = ''" completionHandler:nil];
                smokeSendURL();
                dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ smokePoll(); });
                return;
            }
            if ([value isKindOfClass:NSString.class] && [value length] > 0) {
                if (![value isEqualToString:@"ok"]) {
                    fprintf(stderr, "SMOKE FAILED: %s\n", [value UTF8String]);
                    exit(2);
                }
                NSWindow *original = passage.window;
                WKWebView *view = passage.webview;
                [original performClose:nil];
                if (original.visible) { fprintf(stderr, "SMOKE FAILED: close\n"); exit(2); }
                NSAppleEventDescriptor *target = [NSAppleEventDescriptor descriptorWithProcessIdentifier:NSProcessInfo.processInfo.processIdentifier];
                NSAppleEventDescriptor *event = [NSAppleEventDescriptor appleEventWithEventClass:kCoreEventClass
                    eventID:kAEReopenApplication targetDescriptor:target returnID:kAutoGenerateReturnID transactionID:kAnyTransactionID];
                NSError *sendError = nil;
                [event sendEventWithOptions:NSAppleEventSendNoReply timeout:1 error:&sendError];
                if (sendError) { fprintf(stderr, "SMOKE FAILED: reopen event\n"); exit(2); }
                smokeAttempts = 0;
                smokeReopened(original, view, NO);
            } else {
                dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ smokePoll(); });
            }
        }];
    } else {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ smokePoll(); });
    }
}
__attribute__((constructor)) static void smokeStart(void) {
    [[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationDidFinishLaunchingNotification
        object:nil queue:nil usingBlock:^(NSNotification *notification) { smokeSendURL(); }];
    dispatch_async(dispatch_get_main_queue(), ^{ smokePoll(); });
}
