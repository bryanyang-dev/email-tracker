//go:build darwin && cgo

package credentials

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include "keychain_darwin.h"
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"
)

type KeychainStore struct {
	service string
	account string
	mu      sync.Mutex
	cached  *OAuthCredential
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return *s.cached, nil
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
	s.cached = &credential
	return credential, nil
}

func (s *KeychainStore) Save(ctx context.Context, credential OAuthCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.cached = &credential
	return nil
}

func (s *KeychainStore) Cache(credential OAuthCredential) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = &credential
}

func (s *KeychainStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	service := C.CString(s.service)
	account := C.CString(s.account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(account))

	status := C.lew_keychain_delete(service, account)
	if status == C.errSecSuccess || status == C.errSecItemNotFound {
		s.cached = nil
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
