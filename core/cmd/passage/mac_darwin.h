#include <stdlib.h>

void passageShowWindow(const char *html);
void passageReply(const char *script);
void passageError(const char *message);
const char *passageLanguage(void);
int passageCanOpen(const char *scheme);
void passageCall(char *message);
void passageQuit(void);
void passageTerminate(void);
void passageTerminationReady(void);
void passageOpen(void);
void passagePrepare(void);
int passageWasLoginLaunch(void);
int passageAutostart(void);
char *passageSetAutostart(int enabled);
