package store

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yesabhishek/pastebin-cli/internal/errs"
)

// apiClient is a small HTTP helper shared by the GitLab and Bitbucket
// backends. Credentials are only ever placed in request headers.
type apiClient struct {
	base    string
	client  *http.Client
	auth    func(*http.Request)
	service string // for error messages, e.g. "GitLab"
}

type apiResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func newAPIClient(base, service string, auth func(*http.Request)) *apiClient {
	return &apiClient{
		base:    strings.TrimRight(base, "/"),
		client:  &http.Client{Timeout: 60 * time.Second},
		auth:    auth,
		service: service,
	}
}

func (c *apiClient) do(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+endpoint, body)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "build "+c.service+" request", err)
	}
	req.Header.Set("User-Agent", "pb-cli")
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.auth != nil {
		c.auth(req)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.Wrap(errs.CodeNetwork, c.service+" request cancelled", ctx.Err())
		}
		return nil, errs.Wrap(errs.CodeNetwork, c.service+" network request failed", sanitizeNetErr(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errs.Wrap(errs.CodeNetwork, "read "+c.service+" response", err)
	}
	return &apiResponse{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// classify converts a non-2xx response into a coded error. conflictHints are
// lower-case substrings that mark a 400 response as a write conflict.
func (c *apiClient) classify(r *apiResponse, message string, conflictHints ...string) error {
	detail := fmt.Sprintf("%s API %d", c.service, r.Status)
	if message != "" {
		detail += ": " + message
	}
	lower := strings.ToLower(message)
	switch {
	case r.Status == http.StatusUnauthorized:
		return errs.Wrap(errs.CodeAuth, c.service+" rejected the token. Run `pb login` again for this account", nil)
	case r.Status == http.StatusNotFound:
		return errs.Wrap(errs.CodeNotFound, c.service+" resource not found", nil)
	case r.Status == http.StatusConflict || r.Status == http.StatusPreconditionFailed:
		return errs.Wrap(errs.CodeConflict, detail, nil)
	case r.Status == http.StatusTooManyRequests:
		return errs.Wrap(errs.CodeRateLimit, c.service+" API rate limit reached; try again later", nil)
	case r.Status == http.StatusForbidden:
		return errs.Wrap(errs.CodeAuth, detail+" (does the token have the required scopes?)", nil)
	case r.Status == http.StatusBadRequest || r.Status == http.StatusUnprocessableEntity:
		for _, hint := range conflictHints {
			if strings.Contains(lower, hint) {
				return errs.Wrap(errs.CodeConflict, detail, nil)
			}
		}
		return errs.Wrap(errs.CodeUnknown, detail, nil)
	case r.Status >= 500:
		return errs.Wrap(errs.CodeNetwork, detail, nil)
	default:
		return errs.Wrap(errs.CodeUnknown, detail, nil)
	}
}

// Identity is the account a token belongs to.
type Identity struct {
	Login string
	Name  string
}
