package credentials

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu         sync.RWMutex
	credential OAuthCredential
	present    bool
}

func (s *MemoryStore) Load(context.Context) (OAuthCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.present {
		return OAuthCredential{}, ErrNotFound
	}
	return s.credential, nil
}

func (s *MemoryStore) Save(_ context.Context, credential OAuthCredential) error {
	s.Cache(credential)
	return nil
}

func (s *MemoryStore) Cache(credential OAuthCredential) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = credential
	s.present = true
}

func (s *MemoryStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = OAuthCredential{}
	s.present = false
	return nil
}
