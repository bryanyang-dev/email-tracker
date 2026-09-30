package credentials

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("credentials not found")

type OAuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	Expiry       time.Time `json:"expiry"`
	EmailAddress string    `json:"email_address"`
}

type Store interface {
	Load(context.Context) (OAuthCredential, error)
	Save(context.Context, OAuthCredential) error
	Cache(OAuthCredential)
	Delete(context.Context) error
}
