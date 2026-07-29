package oauthstate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	want := State{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ClientID != want.ClientID ||
		got.ClientSecret != want.ClientSecret ||
		got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken ||
		got.TokenType != want.TokenType ||
		!got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestSaveCreatesFileWithMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Save(path, State{ClientID: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want %o", perm, 0o600)
	}
}

func TestLoadMissingFileReturnsErrMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	_, err := Load(path)
	if !errors.Is(err, ErrMissing) {
		t.Errorf("Load(missing) error = %v, want ErrMissing", err)
	}
}

func TestLoadOptionalMissingFileReturnsZeroState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	got, err := LoadOptional(path)
	if err != nil {
		t.Fatalf("LoadOptional(missing) error = %v, want nil", err)
	}
	if got != (State{}) {
		t.Errorf("LoadOptional(missing) = %+v, want zero State", got)
	}
}

func TestSaveOverwritesWithCompleteNewContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	old := State{ClientID: "old-client", AccessToken: "old-token"}
	if err := Save(path, old); err != nil {
		t.Fatalf("Save (old): %v", err)
	}

	newState := State{ClientID: "new-client", AccessToken: "new-token", RefreshToken: "new-refresh"}
	if err := Save(path, newState); err != nil {
		t.Fatalf("Save (new): %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ClientID != newState.ClientID || got.AccessToken != newState.AccessToken || got.RefreshToken != newState.RefreshToken {
		t.Errorf("Load() after overwrite = %+v, want %+v", got, newState)
	}
	if got.ClientID == old.ClientID {
		t.Errorf("Load() after overwrite still shows old content")
	}
}
