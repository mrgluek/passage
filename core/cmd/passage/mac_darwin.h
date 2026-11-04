#include <stdlib.h>

void passageShowWindow(const char *html);
void passageReply(const char *script);
void passageError(const char *message);
const char *passageLanguage(void);
void passageCall(char *message);
void passageQuit(void);
void passageOpen(void);
void passagePrepare(void);
int passageWasLoginLaunch(void);
int passageAutostart(void);
char *passageSetAutostart(int enabled);
