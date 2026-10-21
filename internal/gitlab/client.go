// Package gitlab opens merge requests through the GitLab REST API, for
// self-managed instances as well as gitlab.com.
package gitlab

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

// Client opens merge requests on one GitLab instance. BaseURL is the
// instance root, e.g. http://192.168.138.192.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type mergeRequest struct {
	WebURL string `json:"web_url"`
}

type mergeRequestRequest struct {
	SourceBranch       string `json:"source_branch"`
	TargetBranch       string `json:"target_branch"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	RemoveSourceBranch bool   `json:"remove_source_branch"`
}

// OpenMergeRequest opens a merge request in project (its path, e.g.
// group/sub/repo) from source into target and returns its web URL. When one
// is already open for source it returns that one, so a retry never opens a
// second.
func (c *Client) OpenMergeRequest(ctx context.Context, project, source, target, title, description string) (string, error) {
	path := "/api/v4/projects/" + url.PathEscape(project) + "/merge_requests"
	var open []mergeRequest
	query := url.Values{"source_branch": {source}, "target_branch": {target}, "state": {"opened"}}
	if err := c.sendJSON(ctx, http.MethodGet, path+"?"+query.Encode(), "GET merge requests", nil, &open); err != nil {
		return "", err
	}
	if len(open) > 0 && open[0].WebURL != "" {
		return open[0].WebURL, nil
	}
	var created mergeRequest
	request := mergeRequestRequest{SourceBranch: source, TargetBranch: target, Title: title, Description: description, RemoveSourceBranch: true}
	if err := c.sendJSON(ctx, http.MethodPost, path, "POST merge request", request, &created); err != nil {
		return "", err
	}
	if created.WebURL == "" {
		return "", fmt.Errorf("POST merge request: GitLab response has no merge request link")
	}
	return created.WebURL, nil
}

// ProjectPath returns the GitLab project path of a git remote URL, e.g.
// "byb-sunline/icore/bnc_busi" for
// http://192.168.138.192/byb-sunline/icore/bnc_busi.git. It accepts HTTP(S)
// remotes, with or without credentials, and SSH remotes like
// git@host:group/repo.git.
func ProjectPath(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	var path string
	switch {
	case strings.Contains(remote, "://"):
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", fmt.Errorf("parse git remote: %w", err)
		}
		path = parsed.Path
	case strings.Contains(remote, ":"):
		_, path, _ = strings.Cut(remote, ":")
	default:
		return "", fmt.Errorf("git remote %q is not a URL", remote)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !strings.Contains(path, "/") {
		return "", fmt.Errorf("git remote %q has no group/project path", remote)
	}
	return path, nil
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
	httpRequest, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	httpRequest.Header.Set("PRIVATE-TOKEN", c.Token)
	httpRequest.Header.Set("Accept", "application/json")
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
		return fmt.Errorf("%s: GitLab returned HTTP %d", operation, httpResponse.StatusCode)
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(response); err != nil {
		return fmt.Errorf("%s: decode response: %w", operation, err)
	}
	return nil
}
