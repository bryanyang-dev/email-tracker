#ifndef LOCAL_EMAIL_WORKSPACE_KEYCHAIN_DARWIN_H
#define LOCAL_EMAIL_WORKSPACE_KEYCHAIN_DARWIN_H

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

OSStatus lew_keychain_save(
    const char *service,
    const char *account,
    const unsigned char *bytes,
    CFIndex length
);

OSStatus lew_keychain_load(
    const char *service,
    const char *account,
    unsigned char **bytes,
    CFIndex *length
);

OSStatus lew_keychain_delete(const char *service, const char *account);
char *lew_status_message(OSStatus status);

#endif
