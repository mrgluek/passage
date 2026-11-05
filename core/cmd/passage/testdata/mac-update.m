// Appended by scripts/test-mac-update.py.
static int updateAttempts;
static void updatePoll(void) {
    if (++updateAttempts > 1800) {
        [@"update timed out" writeToFile:NSProcessInfo.processInfo.environment[@"PASSAGE_UPDATE_TEST_RESULT"]
            atomically:YES encoding:NSUTF8StringEncoding error:nil];
        passageQuit();
        return;
    }
    if (passage.webview) {
        [passage.webview evaluateJavaScript:@"window.passageUpdateResult || ''" completionHandler:^(id value, NSError *error) {
            if ([value isKindOfClass:NSString.class] && [value length] > 0) {
                [value writeToFile:NSProcessInfo.processInfo.environment[@"PASSAGE_UPDATE_TEST_RESULT"]
                    atomically:YES encoding:NSUTF8StringEncoding error:nil];
                passageQuit();
            } else {
                dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ updatePoll(); });
            }
        }];
    } else {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 100 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{ updatePoll(); });
    }
}
__attribute__((constructor)) static void updateStart(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ updatePoll(); });
}
