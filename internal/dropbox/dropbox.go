// Package dropbox is a minimal Dropbox API v2 client covering exactly what the
// Pak needs: PKCE authorisation without a browser on the device, token
// refresh, folder listing, download and upload.
//
// Authorisation uses the "no redirect" variant of the OAuth code flow. The
// user opens the URL on any other device, Dropbox shows them a short code, and
// they type it into the handheld. Combined with PKCE this needs no client
// secret, which matters because a Pak's contents are readable on the SD card.
package dropbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	authorizeURL = "https://www.dropbox.com/oauth2/authorize"
	tokenURL     = "https://api.dropboxapi.com/oauth2/token"
	rpcBase      = "https://api.dropboxapi.com/2"
	contentBase  = "https://content.dropboxapi.com/2"

	// maxUploadSize is Dropbox's limit for the simple (non-session) upload
	// endpoint. Battery saves are kilobytes, so the simple path always suffices
	// and anything larger is a bug worth surfacing.
	maxUploadSize = 150 * 1024 * 1024
)

// Scopes the Pak requests. Note that reading Delta's folder additionally
// requires the Dropbox app to be registered with "Full Dropbox" access, since
// /Apps/Delta Emulator belongs to Delta's own app folder.
// account_info.read is granted to every Dropbox app automatically, but because
// an explicit scope parameter restricts the token to exactly what is listed,
// omitting it here broke users/get_current_account with missing_scope.
var Scopes = []string{
	"account_info.read",
	"files.metadata.read",
	"files.content.read",
	"files.content.write",
}

// Token is the credential set persisted between runs.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	AccountID    string    `json:"account_id,omitempty"`
}

// Valid reports whether the access token can still be used, leaving a margin
// so a long sync does not expire mid-flight.
func (t *Token) Valid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return true
	}
	return time.Now().Add(2 * time.Minute).Before(t.Expiry)
}

// FileMetadata is the subset of Dropbox file metadata the Pak relies on.
//
// ContentHash is Dropbox's own block-based hash, not SHA-1; it is used only to
// detect that remote bytes changed, never as a Delta-compatible checksum.
type FileMetadata struct {
	Name           string    `json:"name"`
	PathLower      string    `json:"path_lower"`
	PathDisplay    string    `json:"path_display"`
	ID             string    `json:"id"`
	Rev            string    `json:"rev"`
	Size           int64     `json:"size"`
	ServerModified time.Time `json:"server_modified"`
	ClientModified time.Time `json:"client_modified"`
	ContentHash    string    `json:"content_hash"`
	Tag            string    `json:".tag"`
}

// IsFile reports whether the entry is a file rather than a folder or deletion.
func (f FileMetadata) IsFile() bool { return f.Tag == "file" }

// Client talks to Dropbox on behalf of one linked account.
type Client struct {
	AppKey string

	HTTP *http.Client

	mu    sync.Mutex
	token Token

	// OnTokenRefresh is invoked whenever a new access token is obtained, so the
	// caller can persist it.
	OnTokenRefresh func(Token)
}

// New builds a client for an app key and an existing token.
func New(appKey string, token Token) *Client {
	return &Client{
		AppKey: appKey,
		HTTP:   newHTTPClient(120 * time.Second),
		token:  token,
	}
}

// Token returns a copy of the current credentials.
func (c *Client) Token() Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// --- Authorisation -------------------------------------------------------

// PKCE holds a generated verifier/challenge pair for one authorisation attempt.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a code verifier and its S256 challenge.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("generating PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// AuthorizeURL builds the URL the user opens on a phone or computer.
func AuthorizeURL(appKey string, p PKCE) string {
	q := url.Values{}
	q.Set("client_id", appKey)
	q.Set("response_type", "code")
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("token_access_type", "offline") // ask for a refresh token
	q.Set("scope", strings.Join(Scopes, " "))
	return authorizeURL + "?" + q.Encode()
}

// Exchange trades the code the user typed in for a token pair.
func Exchange(ctx context.Context, httpClient *http.Client, appKey, code string, p PKCE) (Token, error) {
	form := url.Values{}
	form.Set("code", strings.TrimSpace(code))
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", appKey)
	form.Set("code_verifier", p.Verifier)

	return postToken(ctx, httpClient, form)
}

func postToken(ctx context.Context, httpClient *http.Client, form url.Values) (Token, error) {
	if httpClient == nil {
		httpClient = newHTTPClient(60 * time.Second)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("contacting Dropbox: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("dropbox token request failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		AccountID    string `json:"account_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Token{}, fmt.Errorf("decoding Dropbox token response: %w", err)
	}
	tok := Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		AccountID:    payload.AccountID,
	}
	if payload.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	if tok.AccessToken == "" {
		return Token{}, fmt.Errorf("dropbox returned an empty access token")
	}
	return tok, nil
}

// refreshLocked obtains a fresh access token. Caller must hold c.mu.
func (c *Client) refreshLocked(ctx context.Context) error {
	if c.token.RefreshToken == "" {
		return fmt.Errorf("dropbox session expired and no refresh token is stored; re-link the account")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.token.RefreshToken)
	form.Set("client_id", c.AppKey)

	tok, err := postToken(ctx, c.HTTP, form)
	if err != nil {
		return err
	}
	// A refresh response omits the refresh token; keep the existing one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = c.token.RefreshToken
	}
	if tok.AccountID == "" {
		tok.AccountID = c.token.AccountID
	}
	c.token = tok
	if c.OnTokenRefresh != nil {
		c.OnTokenRefresh(tok)
	}
	return nil
}

func (c *Client) authHeader(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.token.Valid() {
		if err := c.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	return "Bearer " + c.token.AccessToken, nil
}

// --- API calls -----------------------------------------------------------

// APIError is a structured Dropbox error response.
type APIError struct {
	Status  int
	Summary string
	Body    string
}

func (e *APIError) Error() string {
	if e.Summary != "" {
		return fmt.Sprintf("dropbox error %d: %s", e.Status, e.Summary)
	}
	return fmt.Sprintf("dropbox error %d: %s", e.Status, e.Body)
}

// IsNotFound reports whether the error is Dropbox's path/not_found.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return strings.Contains(apiErr.Body, "not_found") || strings.Contains(apiErr.Summary, "not_found")
}

func (c *Client) doRPC(ctx context.Context, endpoint string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.withRetry(ctx, func(auth string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcBase+endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("calling %s: %w", endpoint, err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if resp.StatusCode != http.StatusOK {
			return apiError(resp.StatusCode, body)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	})
}

func apiError(status int, body []byte) error {
	e := &APIError{Status: status, Body: strings.TrimSpace(string(body))}
	var parsed struct {
		Summary string `json:"error_summary"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		e.Summary = parsed.Summary
	}
	return e
}

// withRetry runs fn with a valid auth header, refreshing once on 401 and
// backing off on rate limits and transient server errors.
func (c *Client) withRetry(ctx context.Context, fn func(auth string) error) error {
	const attempts = 4
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		auth, err := c.authHeader(ctx)
		if err != nil {
			return err
		}
		err = fn(auth)
		if err == nil {
			return nil
		}
		lastErr = err

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return err // network/JSON problem: no point retrying blindly
		}
		switch {
		case apiErr.Status == http.StatusUnauthorized:
			// Force a refresh on the next pass.
			c.mu.Lock()
			c.token.Expiry = time.Now().Add(-time.Minute)
			c.mu.Unlock()
		case apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500:
			// fall through to backoff
		default:
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
	}
	return lastErr
}

// ListFolder returns every file directly inside path, following pagination.
// A missing folder yields an empty slice rather than an error, which lets the
// caller distinguish "Delta has never synced" from a real failure.
func (c *Client) ListFolder(ctx context.Context, path string) ([]FileMetadata, error) {
	type listResp struct {
		Entries []FileMetadata `json:"entries"`
		Cursor  string         `json:"cursor"`
		HasMore bool           `json:"has_more"`
	}

	var out []FileMetadata
	var resp listResp
	err := c.doRPC(ctx, "/files/list_folder", map[string]any{
		"path":      normalizePath(path),
		"recursive": false,
		"limit":     2000,
	}, &resp)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out = append(out, resp.Entries...)

	for resp.HasMore {
		cursor := resp.Cursor
		resp = listResp{}
		if err := c.doRPC(ctx, "/files/list_folder/continue", map[string]any{"cursor": cursor}, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Entries...)
	}
	return out, nil
}

// Download fetches a file's contents.
func (c *Client) Download(ctx context.Context, path string) ([]byte, FileMetadata, error) {
	arg, err := json.Marshal(map[string]string{"path": normalizePath(path)})
	if err != nil {
		return nil, FileMetadata{}, err
	}

	var data []byte
	var meta FileMetadata
	err = c.withRetry(ctx, func(auth string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, contentBase+"/files/download", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Dropbox-API-Arg", string(arg))

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("downloading %s: %w", path, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return apiError(resp.StatusCode, body)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		data = body
		if result := resp.Header.Get("Dropbox-API-Result"); result != "" {
			_ = json.Unmarshal([]byte(result), &meta)
		}
		return nil
	})
	if err != nil {
		return nil, FileMetadata{}, err
	}
	return data, meta, nil
}

// Upload writes data to path, overwriting whatever is there.
func (c *Client) Upload(ctx context.Context, path string, data []byte, clientModified time.Time) (FileMetadata, error) {
	if len(data) > maxUploadSize {
		return FileMetadata{}, fmt.Errorf("file %s is %d bytes, above the %d byte single-request upload limit", path, len(data), maxUploadSize)
	}
	argMap := map[string]any{
		"path":       normalizePath(path),
		"mode":       "overwrite",
		"autorename": false,
		"mute":       true,
	}
	if !clientModified.IsZero() {
		// Dropbox rejects sub-second precision and future timestamps.
		argMap["client_modified"] = clientModified.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
	}
	arg, err := json.Marshal(argMap)
	if err != nil {
		return FileMetadata{}, err
	}

	var meta FileMetadata
	err = c.withRetry(ctx, func(auth string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, contentBase+"/files/upload", bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Dropbox-API-Arg", string(arg))
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("uploading %s: %w", path, err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return apiError(resp.StatusCode, body)
		}
		return json.Unmarshal(body, &meta)
	})
	if err != nil {
		return FileMetadata{}, err
	}
	return meta, nil
}

// CurrentAccount returns the linked account's display name, used to confirm
// the link succeeded and to show who is connected in the Pak UI.
func (c *Client) CurrentAccount(ctx context.Context) (string, error) {
	var resp struct {
		Name struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
		Email string `json:"email"`
	}
	// get_current_account takes a null argument rather than an object.
	if err := c.doRPC(ctx, "/users/get_current_account", nil, &resp); err != nil {
		return "", err
	}
	if resp.Name.DisplayName != "" {
		return resp.Name.DisplayName, nil
	}
	return resp.Email, nil
}

// normalizePath converts a user-facing path into the form Dropbox expects:
// a leading slash, no trailing slash, and "" for the root.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
