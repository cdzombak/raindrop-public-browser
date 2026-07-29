package refresh

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rd "github.com/cdzombak/raindrop-io-api-client/pkg/raindrop"

	"github.com/cdzombak/raindrop-public-browser/internal/oauthstate"
)

// tokenExpiryBuffer refreshes slightly early to avoid using a token that
// expires mid-refresh.
const tokenExpiryBuffer = 60 * time.Second

// OAuthTokenSource provides access tokens from the OAuth state file,
// refreshing them via the Raindrop API and writing refreshed tokens back to
// the state file.
type OAuthTokenSource struct {
	client    *rd.Client
	statePath string
	logger    *slog.Logger
	clock     func() time.Time

	mu    sync.Mutex
	state oauthstate.State
}

// NewOAuthTokenSource builds a token source from a loaded state file.
func NewOAuthTokenSource(statePath string, state oauthstate.State, logger *slog.Logger) (*OAuthTokenSource, error) {
	// Redirect URI is only needed for the interactive authorize flow, not
	// for token refresh.
	client, err := rd.NewClientWithLogger(state.ClientID, state.ClientSecret, "", logger)
	if err != nil {
		return nil, fmt.Errorf("create raindrop client: %w", err)
	}
	return &OAuthTokenSource{
		client:    client,
		statePath: statePath,
		logger:    logger,
		clock:     time.Now,
		state:     state,
	}, nil
}

// Token returns a valid access token, refreshing and persisting a new one
// when the stored token is missing or (about to be) expired.
func (t *OAuthTokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock()
	if t.state.AccessToken != "" && !t.state.ExpiresAt.IsZero() && now.Before(t.state.ExpiresAt.Add(-tokenExpiryBuffer)) {
		return t.state.AccessToken, nil
	}
	if t.state.RefreshToken == "" {
		return "", fmt.Errorf("no valid access token and no refresh token; run the login subcommand to re-authenticate")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := t.client.RefreshAccessToken(t.state.RefreshToken, ctx)
	if err != nil {
		return "", fmt.Errorf("refreshing access token: %w", err)
	}
	if tok.AccessToken == "" {
		msg := tok.ErrorMessage
		if msg == "" {
			msg = tok.Error
		}
		if msg == "" {
			msg = "unknown error"
		}
		return "", fmt.Errorf("token refresh failed: %s (run the login subcommand to re-authenticate)", msg)
	}

	t.state.ApplyToken(tok, t.clock())
	if err := oauthstate.Save(t.statePath, t.state); err != nil {
		return "", fmt.Errorf("persisting refreshed token: %w", err)
	}
	t.logger.Info("refreshed Raindrop access token", "expires_at", t.state.ExpiresAt)
	return t.state.AccessToken, nil
}
