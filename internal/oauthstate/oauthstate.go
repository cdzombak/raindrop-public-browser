// Package oauthstate persists Raindrop OAuth credentials in a JSON state
// file, written atomically and 0600 because it holds a client secret and
// refresh token.
package oauthstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	rd "github.com/cdzombak/raindrop-io-api-client/pkg/raindrop"
)

// ErrMissing indicates the OAuth state file does not exist or is empty.
var ErrMissing = errors.New("oauth state missing or empty")

// State is everything needed to make authenticated, non-interactive Raindrop
// API calls: the app credentials plus the current token set. Storing the
// client credentials alongside the tokens keeps the state file
// self-sufficient.
type State struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Load reads and parses the OAuth state file. A missing or empty file is
// reported as ErrMissing so callers can distinguish it from a parse error.
func Load(path string) (State, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is the operator-configured state file
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrMissing
		}
		return State{}, fmt.Errorf("read oauth state: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return State{}, ErrMissing
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parse oauth state %q: %w", path, err)
	}
	return s, nil
}

// LoadOptional returns a zero State when the file is missing or empty, and an
// error only for genuine read/parse failures.
func LoadOptional(path string) (State, error) {
	s, err := Load(path)
	if errors.Is(err, ErrMissing) {
		return State{}, nil
	}
	return s, err
}

// Save writes the state file, creating parent directories as needed. The file
// may contain a client secret and refresh token, so it is written 0600.
func Save(path string, s State) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create oauth state dir: %w", err)
		}
	}
	b, err := json.MarshalIndent(s, "", "  ") //nolint:gosec // persisting the secret is this file's purpose; written 0600
	if err != nil {
		return err
	}
	b = append(b, '\n')
	// Written atomically: a crash mid-write must not corrupt these — unlike
	// rendered pages, tokens cannot be regenerated and a loss forces a manual
	// re-login.
	if err := atomicWriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write oauth state: %w", err)
	}
	return nil
}

// ApplyToken updates the token portion of the state from an API token
// response, computing the absolute expiry from ExpiresIn.
func (s *State) ApplyToken(t *rd.AccessTokenResponse, now time.Time) {
	s.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		s.RefreshToken = t.RefreshToken
	}
	if t.TokenType != "" {
		s.TokenType = t.TokenType
	}
	if t.ExpiresIn > 0 {
		s.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	} else {
		s.ExpiresAt = time.Time{}
	}
}

// atomicWriteFile writes data to a temp file in the destination directory,
// then renames it over path. A reader never observes a partially written
// file, and a crash mid-write leaves the previous contents intact.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("setting temp file permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("moving temp file into place: %w", err)
	}
	return nil
}
