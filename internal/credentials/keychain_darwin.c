#include "keychain_darwin.h"

#include <stdlib.h>
#include <string.h>

static CFStringRef lew_string(const char *value) {
    return CFStringCreateWithCString(
        kCFAllocatorDefault,
        value,
        kCFStringEncodingUTF8
    );
}

static CFMutableDictionaryRef lew_query(
    const char *service_value,
    const char *account_value
) {
    CFStringRef service = lew_string(service_value);
    CFStringRef account = lew_string(account_value);
    if (service == NULL || account == NULL) {
        if (service != NULL) CFRelease(service);
        if (account != NULL) CFRelease(account);
        return NULL;
    }

    CFMutableDictionaryRef query = CFDictionaryCreateMutable(
        kCFAllocatorDefault,
        0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );
    if (query != NULL) {
        CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
        CFDictionarySetValue(query, kSecAttrService, service);
        CFDictionarySetValue(query, kSecAttrAccount, account);
    }
    CFRelease(service);
    CFRelease(account);
    return query;
}

OSStatus lew_keychain_save(
    const char *service,
    const char *account,
    const unsigned char *bytes,
    CFIndex length
) {
    CFMutableDictionaryRef query = lew_query(service, account);
    if (query == NULL) return errSecAllocate;

    CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, length);
    if (data == NULL) {
        CFRelease(query);
        return errSecAllocate;
    }

    const void *update_keys[] = {kSecValueData};
    const void *update_values[] = {data};
    CFDictionaryRef updates = CFDictionaryCreate(
        kCFAllocatorDefault,
        update_keys,
        update_values,
        1,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );
    if (updates == NULL) {
        CFRelease(data);
        CFRelease(query);
        return errSecAllocate;
    }

    OSStatus status = SecItemUpdate(query, updates);
    if (status == errSecItemNotFound) {
        CFDictionarySetValue(query, kSecValueData, data);
        CFDictionarySetValue(
            query,
            kSecAttrLabel,
            CFSTR("Local Email Workspace Gmail authorization")
        );
        status = SecItemAdd(query, NULL);
    }

    CFRelease(updates);
    CFRelease(data);
    CFRelease(query);
    return status;
}

OSStatus lew_keychain_load(
    const char *service,
    const char *account,
    unsigned char **bytes,
    CFIndex *length
) {
    *bytes = NULL;
    *length = 0;

    CFMutableDictionaryRef query = lew_query(service, account);
    if (query == NULL) return errSecAllocate;
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);

    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    CFRelease(query);
    if (status != errSecSuccess) return status;
    if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
        if (result != NULL) CFRelease(result);
        return errSecDecode;
    }

    CFDataRef data = (CFDataRef)result;
    CFIndex data_length = CFDataGetLength(data);
    unsigned char *copy = malloc((size_t)data_length);
    if (copy == NULL && data_length > 0) {
        CFRelease(result);
        return errSecAllocate;
    }
    if (data_length > 0) {
        memcpy(copy, CFDataGetBytePtr(data), (size_t)data_length);
    }
    *bytes = copy;
    *length = data_length;
    CFRelease(result);
    return errSecSuccess;
}

OSStatus lew_keychain_delete(const char *service, const char *account) {
    CFMutableDictionaryRef query = lew_query(service, account);
    if (query == NULL) return errSecAllocate;
    OSStatus status = SecItemDelete(query);
    CFRelease(query);
    return status;
}

char *lew_status_message(OSStatus status) {
    CFStringRef message = SecCopyErrorMessageString(status, NULL);
    if (message == NULL) return NULL;

    CFIndex size = CFStringGetMaximumSizeForEncoding(
        CFStringGetLength(message),
        kCFStringEncodingUTF8
    ) + 1;
    char *buffer = malloc((size_t)size);
    if (buffer == NULL) {
        CFRelease(message);
        return NULL;
    }
    if (!CFStringGetCString(message, buffer, size, kCFStringEncodingUTF8)) {
        free(buffer);
        buffer = NULL;
    }
    CFRelease(message);
    return buffer;
}
