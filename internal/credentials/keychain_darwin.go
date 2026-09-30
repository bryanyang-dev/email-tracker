//go:build darwin && cgo

package credentials

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFStringRef lew_string(const char *value) {
	return CFStringCreateWithCString(kCFAllocatorDefault, value, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef lew_query(const char *serviceValue, const char *accountValue) {
	CFStringRef service = lew_string(serviceValue);
	CFStringRef account = lew_string(accountValue);
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

static OSStatus lew_keychain_save(
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

	const void *updateKeys[] = { kSecValueData };
	const void *updateValues[] = { data };
	CFDictionaryRef updates = CFDictionaryCreate(
		kCFAllocatorDefault,
		updateKeys,
		updateValues,
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
		CFDictionarySetValue(query, kSecAttrLabel, CFSTR("Local Email Workspace Gmail authorization"));
		status = SecItemAdd(query, NULL);
	}

	CFRelease(updates);
	CFRelease(data);
	CFRelease(query);
	return status;
}

static OSStatus lew_keychain_load(
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
	CFIndex dataLength = CFDataGetLength(data);
	unsigned char *copy = malloc((size_t)dataLength);
	if (copy == NULL && dataLength > 0) {
		CFRelease(result);
		return errSecAllocate;
	}
	if (dataLength > 0) {
		memcpy(copy, CFDataGetBytePtr(data), (size_t)dataLength);
	}
	*bytes = copy;
	*length = dataLength;
	CFRelease(result);
	return errSecSuccess;
}

static OSStatus lew_keychain_delete(const char *service, const char *account) {
	CFMutableDictionaryRef query = lew_query(service, account);
	if (query == NULL) return errSecAllocate;
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}

static char *lew_status_message(OSStatus status) {
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
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"unsafe"
)

type KeychainStore struct {
	service string
	account string
}

func NewKeychainStore(service, account string) (*KeychainStore, error) {
	if service == "" || account == "" {
		return nil, fmt.Errorf("Keychain service and account are required")
	}
	return &KeychainStore{service: service, account: account}, nil
}

func (s *KeychainStore) Load(ctx context.Context) (OAuthCredential, error) {
	if err := ctx.Err(); err != nil {
		return OAuthCredential{}, err
	}
	service := C.CString(s.service)
	account := C.CString(s.account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))

	var bytes *C.uchar
	var length C.CFIndex
	status := C.lew_keychain_load(service, account, &bytes, &length)
	if status == C.errSecItemNotFound {
		return OAuthCredential{}, ErrNotFound
	}
	if status != C.errSecSuccess {
		return OAuthCredential{}, keychainError("read credential", status)
	}
	if bytes != nil {
		defer C.free(unsafe.Pointer(bytes))
	}

	encoded := C.GoBytes(unsafe.Pointer(bytes), C.int(length))
	var credential OAuthCredential
	if err := json.Unmarshal(encoded, &credential); err != nil {
		return OAuthCredential{}, fmt.Errorf("decode Keychain credential: %w", err)
	}
	return credential, nil
}

func (s *KeychainStore) Save(ctx context.Context, credential OAuthCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}

	service := C.CString(s.service)
	account := C.CString(s.account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))

	status := C.lew_keychain_save(
		service,
		account,
		(*C.uchar)(unsafe.Pointer(&encoded[0])),
		C.CFIndex(len(encoded)),
	)
	if status != C.errSecSuccess {
		return keychainError("write credential", status)
	}
	return nil
}

func (s *KeychainStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	service := C.CString(s.service)
	account := C.CString(s.account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))

	status := C.lew_keychain_delete(service, account)
	if status == C.errSecSuccess || status == C.errSecItemNotFound {
		return nil
	}
	return keychainError("delete credential", status)
}

func keychainError(action string, status C.OSStatus) error {
	message := C.lew_status_message(status)
	if message == nil {
		return fmt.Errorf("%s in Keychain: OSStatus %d", action, int32(status))
	}
	defer C.free(unsafe.Pointer(message))
	return fmt.Errorf("%s in Keychain: %s (OSStatus %d)", action, C.GoString(message), int32(status))
}
