package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	rd "github.com/cdzombak/raindrop-io-api-client/pkg/raindrop"

	"github.com/cdzombak/raindrop-public-browser/internal/config"
	"github.com/cdzombak/raindrop-public-browser/internal/oauthstate"
)

// loginTimeout bounds how long we wait for the user to complete
// authorization.
const loginTimeout = 5 * time.Minute

// callbackListenAddr is the address the OAuth callback server binds to.
// Normally this is the redirect URI's host (e.g. localhost:8080), i.e.
// loopback. Inside Docker, where the browser reaches the container through a
// published port, it binds all interfaces on the same port so forwarding
// works; the Dockerfile sets the env variable that requests this.
func callbackListenAddr(redirect *url.URL, inDocker bool) string {
	if !inDocker {
		return redirect.Host
	}
	if port := redirect.Port(); port != "" {
		return "0.0.0.0:" + port
	}
	return redirect.Host
}

// runLogin performs the interactive OAuth flow: it stands up a local HTTP
// server for the redirect callback, directs the user to Raindrop's authorize
// page, exchanges the returned code for tokens, and persists everything to
// the OAuth state file.
func runLogin(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.OAuthStateFile == "" {
		return fmt.Errorf("%s must be set for login", config.EnvOAuthStateFile)
	}

	// Reuse app credentials already stored in the state file so that
	// re-login doesn't require re-supplying them.
	existing, err := oauthstate.LoadOptional(cfg.OAuthStateFile)
	if err != nil {
		return err
	}
	clientID := firstNonEmpty(cfg.ClientID, existing.ClientID)
	clientSecret := firstNonEmpty(cfg.ClientSecret, existing.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("client ID and secret are required for login; set $%s and $%s", config.EnvClientID, config.EnvClientSecret)
	}

	redirect, err := url.Parse(cfg.OAuthRedirect)
	if err != nil {
		return fmt.Errorf("invalid redirect URI %q: %w", cfg.OAuthRedirect, err)
	}
	if redirect.Host == "" || redirect.Path == "" {
		return fmt.Errorf("redirect URI %q must include a host, port, and path", cfg.OAuthRedirect)
	}

	client, err := rd.NewClientWithLogger(clientID, clientSecret, cfg.OAuthRedirect, logger)
	if err != nil {
		return err
	}

	codeCh := make(chan string, 1)
	srvErrCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		code, err := client.GetAuthorizationCode(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "<h1>Authorization failed</h1><p>%s</p>", html.EscapeString(err.Error()))
			trySend(srvErrCh, err)
			return
		}
		_, _ = fmt.Fprint(w, "<h1>Authorized</h1><p>You may close this tab and return to the terminal.</p>")
		trySendStr(codeCh, code)
	})

	// Bind before printing the URL so we fail fast if the port is
	// unavailable.
	listenAddr := callbackListenAddr(redirect, cfg.InDocker)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("cannot listen on %q for the OAuth callback: %w", listenAddr, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			trySend(srvErrCh, err)
		}
	}()
	defer func() { _ = srv.Close() }()

	authURLObj, err := client.GetAuthorizationURL()
	if err != nil {
		return fmt.Errorf("building authorization URL: %w", err)
	}
	authURL := authURLObj.String()
	fmt.Println("Open the following URL in your browser to authorize this app:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	fmt.Printf("Waiting for the authorization callback on %s ...\n", cfg.OAuthRedirect)
	openBrowser(authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-srvErrCh:
		return fmt.Errorf("authorization failed: %w", err)
	case <-time.After(loginTimeout):
		return fmt.Errorf("timed out after %s waiting for authorization", loginTimeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := client.GetAccessToken(code, ctx)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("token exchange returned no access token (%s)", tok.ErrorMessage)
	}

	state := oauthstate.State{ClientID: clientID, ClientSecret: clientSecret}
	state.ApplyToken(tok, time.Now())
	if err := oauthstate.Save(cfg.OAuthStateFile, state); err != nil {
		return err
	}
	fmt.Printf("Success. OAuth state written to %s\n", cfg.OAuthStateFile)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func trySend(ch chan<- error, v error) {
	select {
	case ch <- v:
	default:
	}
}

func trySendStr(ch chan<- string, v string) {
	select {
	case ch <- v:
	default:
	}
}

// openBrowser makes a best-effort attempt to open u in the user's browser.
func openBrowser(u string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{u}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	default:
		name, args = "xdg-open", []string{u}
	}
	_ = exec.Command(name, args...).Start() //nolint:gosec // fixed command per-OS; args carry only the auth URL
}
