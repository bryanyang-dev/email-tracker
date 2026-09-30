package config

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultAddress = "127.0.0.1:8787"
	defaultUIURL   = "http://127.0.0.1:5173"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Address           string
	UIURL             string
	GmailClientID     string
	GmailClientSecret string
	OAuthRedirectURL  string
}

func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	address := envOrDefault("APP_ADDRESS", defaultAddress)
	uiURL := envOrDefault("APP_UI_URL", defaultUIURL)

	if err := requireLoopbackAddress(address); err != nil {
		return Config{}, fmt.Errorf("APP_ADDRESS: %w", err)
	}
	if err := requireLoopbackURL(uiURL); err != nil {
		return Config{}, fmt.Errorf("APP_UI_URL: %w", err)
	}

	return Config{
		Address:           address,
		UIURL:             uiURL,
		GmailClientID:     os.Getenv("GMAIL_CLIENT_ID"),
		GmailClientSecret: os.Getenv("GMAIL_CLIENT_SECRET"),
		OAuthRedirectURL:  "http://" + address + "/api/v1/auth/gmail/callback",
	}, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	return parseDotEnv(file)
}

func parseDotEnv(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, rawValue, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !environmentName.MatchString(name) {
			return fmt.Errorf("line %d has an invalid variable assignment", lineNumber)
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}

		value, err := parseDotEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("line %d: set variable: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	return nil
}

func parseDotEnvValue(value string) (string, error) {
	if strings.HasPrefix(value, "'") || strings.HasSuffix(value, "'") {
		if len(value) < 2 || value[0] != '\'' || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("invalid quoted value")
		}
		return value[1 : len(value)-1], nil
	}
	if strings.HasPrefix(value, `"`) || strings.HasSuffix(value, `"`) {
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return "", fmt.Errorf("invalid quoted value")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value")
		}
		return decoded, nil
	}
	return value, nil
}

func (c Config) GmailConfigured() bool {
	return c.GmailClientID != ""
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must include a host and port: %w", err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("must bind to literal 127.0.0.1")
	}
	return nil
}

func requireLoopbackURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return fmt.Errorf("must be an http URL on literal 127.0.0.1")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("must not include a path")
	}
	return nil
}
