//go:build !darwin || !cgo

package credentials

import (
	"context"
	"fmt"
)

type KeychainStore struct{}

func NewKeychainStore(_, _ string) (*KeychainStore, error) {
	return nil, fmt.Errorf("the MVP credential store requires macOS with cgo enabled")
}

func (*KeychainStore) Load(context.Context) (OAuthCredential, error) {
	return OAuthCredential{}, fmt.Errorf("macOS Keychain is unavailable")
}

func (*KeychainStore) Save(context.Context, OAuthCredential) error {
	return fmt.Errorf("macOS Keychain is unavailable")
}

func (*KeychainStore) Delete(context.Context) error {
	return fmt.Errorf("macOS Keychain is unavailable")
}
