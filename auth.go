package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshURL         = "https://auth.openai.com/oauth/token"
	refreshSkew        = 30 * time.Second
)

type TokenSource struct {
	codexHome string
	mu        sync.Mutex
}

type CodexToken struct {
	AccessToken string
	AccountID   string
}

func (s *TokenSource) Token(ctx context.Context) (CodexToken, error) {
	path, err := s.authPath()
	if err != nil {
		return CodexToken{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := readAuthFile(path)
	if err != nil {
		return CodexToken{}, err
	}
	state := tokenFromAuth(data)
	if state.validFor(refreshSkew) {
		return state.codexToken(), nil
	}
	if state.RefreshToken == "" {
		return CodexToken{}, errors.New("Codex refresh token is missing; run: codex login")
	}

	newTokens, err := s.refresh(ctx, state.RefreshToken)
	if err != nil {
		return CodexToken{}, err
	}
	mergeTokens(data, newTokens)
	data["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if err := writeAuthFile(path, data); err != nil {
		return CodexToken{}, err
	}

	state = tokenFromAuth(data)
	if state.AccessToken == "" {
		return CodexToken{}, errors.New("token refresh response did not include an access token")
	}
	return state.codexToken(), nil
}

func (s *TokenSource) authPath() (string, error) {
	home := s.codexHome
	if home == "" {
		home = os.Getenv("CODEX_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(home, "auth.json"), nil
}

type authTokenState struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    time.Time
}

func (s authTokenState) validFor(skew time.Duration) bool {
	if s.AccessToken == "" {
		return false
	}
	if s.ExpiresAt.IsZero() {
		return true
	}
	return time.Now().Add(skew).Before(s.ExpiresAt)
}

func (s authTokenState) codexToken() CodexToken {
	return CodexToken{AccessToken: s.AccessToken, AccountID: s.AccountID}
}

func readAuthFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Codex auth file not found at %s; run: codex login", path)
		}
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if stringValue(data, "auth_mode") != "chatgpt" {
		return nil, fmt.Errorf("Codex auth mode is %q, expected %q; run: codex login", stringValue(data, "auth_mode"), "chatgpt")
	}
	if _, ok := data["tokens"].(map[string]any); !ok {
		return nil, errors.New("Codex auth file is missing tokens; run: codex login")
	}
	return data, nil
}

func writeAuthFile(path string, data map[string]any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}

func tokenFromAuth(data map[string]any) authTokenState {
	tokens, _ := data["tokens"].(map[string]any)
	accessToken := stringValue(tokens, "access_token")
	expiresAt, _ := jwtExpiry(accessToken)
	return authTokenState{
		AccessToken:  accessToken,
		RefreshToken: stringValue(tokens, "refresh_token"),
		AccountID:    stringValue(tokens, "account_id"),
		ExpiresAt:    expiresAt,
	}
}

func mergeTokens(data map[string]any, updates map[string]any) {
	tokens, _ := data["tokens"].(map[string]any)
	for _, key := range []string{"access_token", "id_token", "refresh_token"} {
		if value := stringValue(updates, key); value != "" {
			tokens[key] = value
		}
	}
}

func (s *TokenSource) refresh(ctx context.Context, refreshToken string) (map[string]any, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     codexOAuthClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token refresh failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
