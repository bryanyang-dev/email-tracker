package googleapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/observability"
)

const (
	googleAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint         = "https://oauth2.googleapis.com/token"
	gmailReadonlyScope          = "https://www.googleapis.com/auth/gmail.readonly"
)

type OAuthClient struct {
	httpClient   *http.Client
	clientID     string
	clientSecret string
	redirectURL  string
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

func NewOAuthClient(httpClient *http.Client, clientID, clientSecret, redirectURL string) *OAuthClient {
	return &OAuthClient{
		httpClient:   httpClient,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
	}
}

func (c *OAuthClient) AuthorizationURL(state, codeChallenge string) string {
	query := url.Values{
		"access_type":           {"offline"},
		"client_id":             {c.clientID},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"consent"},
		"redirect_uri":          {c.redirectURL},
		"response_type":         {"code"},
		"scope":                 {gmailReadonlyScope},
		"state":                 {state},
	}
	return googleAuthorizationEndpoint + "?" + query.Encode()
}

func (c *OAuthClient) Exchange(ctx context.Context, code, verifier string) (credentials.OAuthCredential, error) {
	values := url.Values{
		"client_id":     {c.clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {c.redirectURL},
	}
	if c.clientSecret != "" {
		values.Set("client_secret", c.clientSecret)
	}

	response, err := c.postToken(ctx, values, "exchange")
	if err != nil {
		return credentials.OAuthCredential{}, err
	}
	if response.RefreshToken == "" {
		return credentials.OAuthCredential{}, fmt.Errorf("Google did not return a refresh token")
	}
	return credentialFromToken(response), nil
}

func (c *OAuthClient) Refresh(ctx context.Context, current credentials.OAuthCredential) (credentials.OAuthCredential, error) {
	if current.RefreshToken == "" {
		return credentials.OAuthCredential{}, fmt.Errorf("no refresh token is available")
	}
	values := url.Values{
		"client_id":     {c.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {current.RefreshToken},
	}
	if c.clientSecret != "" {
		values.Set("client_secret", c.clientSecret)
	}

	response, err := c.postToken(ctx, values, "refresh")
	if err != nil {
		return credentials.OAuthCredential{}, err
	}
	refreshed := credentialFromToken(response)
	refreshed.RefreshToken = current.RefreshToken
	refreshed.EmailAddress = current.EmailAddress
	if refreshed.Scope == "" {
		refreshed.Scope = current.Scope
	}
	return refreshed, nil
}

func (c *OAuthClient) postToken(ctx context.Context, values url.Values, operation string) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	startedAt := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		logOutboundCall(ctx, "google_oauth", operation, http.MethodPost, 0, "error", startedAt)
		return tokenResponse{}, fmt.Errorf("request token: %w", err)
	}
	outcome := "success"
	defer func() {
		response.Body.Close()
		logOutboundCall(ctx, "google_oauth", operation, http.MethodPost, response.StatusCode, outcome, startedAt)
	}()

	if response.StatusCode != http.StatusOK {
		outcome = "error"
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		outcome = "error"
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if token.AccessToken == "" {
		outcome = "error"
		return tokenResponse{}, fmt.Errorf("token response did not contain an access token")
	}
	return token, nil
}

func logOutboundCall(ctx context.Context, service, operation, method string, status int, outcome string, startedAt time.Time) {
	slog.Info("outbound api call completed",
		"request_id", observability.RequestID(ctx),
		"service", service,
		"operation", operation,
		"method", method,
		"status", status,
		"outcome", outcome,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
}

func credentialFromToken(token tokenResponse) credentials.OAuthCredential {
	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return credentials.OAuthCredential{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    tokenType,
		Scope:        token.Scope,
		Expiry:       time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
	}
}

func clampLimit(raw string) int {
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 25
	}
	if limit > 50 {
		return 50
	}
	return limit
}
