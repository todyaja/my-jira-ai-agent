// Package github opens pull requests through the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultAPIURL is the GitHub.com REST API root.
const DefaultAPIURL = "https://api.github.com"

// Client opens pull requests in one repository, Owner/Repo, into Base.
type Client struct {
	// APIURL is the REST API root; empty means DefaultAPIURL.
	APIURL     string
	Owner      string
	Repo       string
	Token      string
	Base       string
	HTTPClient *http.Client
}

type pullRequest struct {
	HTMLURL string `json:"html_url"`
}

type pullRequestRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// OpenPullRequest opens a pull request from branch into Base and returns its
// web URL. When one is already open for branch it returns that one instead,
// so a retried call never opens a second pull request.
func (c *Client) OpenPullRequest(ctx context.Context, branch, title, body string) (string, error) {
	existing, found, err := c.findOpen(ctx, branch)
	if err != nil || found {
		return existing, err
	}
	var created pullRequest
	request := pullRequestRequest{Title: title, Head: branch, Base: c.Base, Body: body}
	if err := c.sendJSON(ctx, http.MethodPost, c.repoPath()+"/pulls", "POST pull request", request, &created); err != nil {
		return "", err
	}
	if created.HTMLURL == "" {
		return "", fmt.Errorf("POST pull request: GitHub response has no pull request link")
	}
	return created.HTMLURL, nil
}

func (c *Client) findOpen(ctx context.Context, branch string) (string, bool, error) {
	var open []pullRequest
	query := url.Values{"head": {c.Owner + ":" + branch}, "state": {"open"}}
	if err := c.sendJSON(ctx, http.MethodGet, c.repoPath()+"/pulls?"+query.Encode(), "GET pull requests", nil, &open); err != nil {
		return "", false, err
	}
	if len(open) == 0 {
		return "", false, nil
	}
	return open[0].HTMLURL, true, nil
}

func (c *Client) repoPath() string {
	return "/repos/" + url.PathEscape(c.Owner) + "/" + url.PathEscape(c.Repo)
}

// sendJSON encodes request (when non-nil) as the body and decodes the
// response into response. Errors never include the response body.
func (c *Client) sendJSON(ctx context.Context, method, path, operation string, request, response any) error {
	var body io.Reader
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("%s: encode request: %w", operation, err)
		}
		body = bytes.NewReader(encoded)
	}
	apiURL := c.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(apiURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	httpRequest.Header.Set("Accept", "application/vnd.github+json")
	httpRequest.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if request != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s: GitHub returned HTTP %d", operation, httpResponse.StatusCode)
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(response); err != nil {
		return fmt.Errorf("%s: decode response: %w", operation, err)
	}
	return nil
}

// ParseRepository splits a repository given as "owner/repo", a bare "repo"
// (with owner supplying the owner), or a URL such as
// https://github.com/owner/repo.git.
func ParseRepository(owner, repo string) (string, string, error) {
	owner, repo = strings.TrimSpace(owner), strings.TrimSpace(repo)
	if parsed, err := url.Parse(repo); err == nil && parsed.Host != "" {
		repo = parsed.Path
	}
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	if before, after, found := strings.Cut(repo, "/"); found {
		owner, repo = before, after
	}
	if owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("GitHub repository %q is not owner/repo", repo)
	}
	return owner, repo, nil
}
