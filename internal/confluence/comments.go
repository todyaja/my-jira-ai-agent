package confluence

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// AppliedProperty marks a comment the agent has applied when it cannot be
// resolved: footer comments have no resolved state, and an inline comment
// whose highlighted text is gone cannot be updated.
const AppliedProperty = "jira-ai-agent.applied"

// Comment is a reviewer's top-level comment on a page.
type Comment struct {
	ID     string
	PageID string
	// Inline comments highlight Selection on the page; Dangling ones lost
	// their highlight because the text was changed.
	Inline    bool
	Dangling  bool
	Created   string
	Text      string
	Selection string
}

type commentResponse struct {
	ID               string `json:"id"`
	ResolutionStatus string `json:"resolutionStatus"`
	Version          struct {
		Number    int    `json:"number"`
		CreatedAt string `json:"createdAt"`
	} `json:"version"`
	Body struct {
		Storage pageBody `json:"storage"`
	} `json:"body"`
	Properties struct {
		InlineOriginalSelection string `json:"inlineOriginalSelection"`
	} `json:"properties"`
}

type commentPage struct {
	Results []commentResponse `json:"results"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// OpenComments returns the page's top-level comments not dealt with yet,
// oldest first: inline comments that are open or reopened, and footer and
// dangling inline comments without the applied property.
func (c *Client) OpenComments(ctx context.Context, pageID string) ([]Comment, error) {
	query := url.Values{"body-format": {"storage"}, "sort": {"created-date"}, "limit": {"100"}}
	footer, err := c.listComments(ctx, "/wiki/api/v2/pages/"+url.PathEscape(pageID)+"/footer-comments?"+query.Encode(), "GET footer comments")
	if err != nil {
		return nil, err
	}
	query["resolution-status"] = []string{"open", "reopened", "dangling"}
	inline, err := c.listComments(ctx, "/wiki/api/v2/pages/"+url.PathEscape(pageID)+"/inline-comments?"+query.Encode(), "GET inline comments")
	if err != nil {
		return nil, err
	}

	comments := []Comment{}
	add := func(response commentResponse, isInline bool) error {
		dangling := response.ResolutionStatus == "dangling"
		if !isInline || dangling {
			applied, err := c.hasApplied(ctx, response.ID)
			if err != nil || applied {
				return err
			}
		}
		comments = append(comments, Comment{
			ID:        response.ID,
			PageID:    pageID,
			Inline:    isInline,
			Dangling:  dangling,
			Created:   response.Version.CreatedAt,
			Text:      StorageText(response.Body.Storage.Value),
			Selection: strings.TrimSpace(response.Properties.InlineOriginalSelection),
		})
		return nil
	}
	for _, response := range footer {
		if err := add(response, false); err != nil {
			return nil, err
		}
	}
	for _, response := range inline {
		if err := add(response, true); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].Created < comments[j].Created })
	return comments, nil
}

// CloseComment marks a comment applied. It replies with reply and resolves
// an open inline comment, replies to a footer comment and sets the applied
// property, and only sets the property on a dangling inline comment, which
// cannot be replied to or resolved. If a later call fails the comment stays
// open, so a retry may reply twice but never skips a comment.
func (c *Client) CloseComment(ctx context.Context, comment Comment, reply string) error {
	if !comment.Dangling {
		kind := "footer-comments"
		if comment.Inline {
			kind = "inline-comments"
		}
		request := map[string]any{
			"parentCommentId": comment.ID,
			"body":            pageBody{Representation: "storage", Value: "<p>" + html.EscapeString(reply) + "</p>"},
		}
		var created any
		if err := c.sendJSON(ctx, http.MethodPost, "/wiki/api/v2/"+kind, "POST comment reply", request, &created); err != nil {
			return err
		}
	}
	if comment.Inline && !comment.Dangling {
		return c.resolveInline(ctx, comment.ID)
	}
	property := map[string]any{"key": AppliedProperty, "value": map[string]bool{"applied": true}}
	var created any
	return c.sendJSON(ctx, http.MethodPost, "/wiki/api/v2/comments/"+url.PathEscape(comment.ID)+"/properties", "POST comment property", property, &created)
}

func (c *Client) resolveInline(ctx context.Context, id string) error {
	var current commentResponse
	path := "/wiki/api/v2/inline-comments/" + url.PathEscape(id)
	if err := c.sendJSON(ctx, http.MethodGet, path+"?body-format=storage", "GET inline comment", nil, &current); err != nil {
		return err
	}
	request := map[string]any{
		"version":  pageVersion{Number: current.Version.Number + 1},
		"body":     pageBody{Representation: "storage", Value: current.Body.Storage.Value},
		"resolved": true,
	}
	var updated any
	return c.sendJSON(ctx, http.MethodPut, path, "PUT inline comment", request, &updated)
}

func (c *Client) hasApplied(ctx context.Context, commentID string) (bool, error) {
	var payload struct {
		Results []struct {
			Key string `json:"key"`
		} `json:"results"`
	}
	query := url.Values{"key": {AppliedProperty}}
	if err := c.sendJSON(ctx, http.MethodGet, "/wiki/api/v2/comments/"+url.PathEscape(commentID)+"/properties?"+query.Encode(), "GET comment property", nil, &payload); err != nil {
		return false, err
	}
	return len(payload.Results) > 0, nil
}

// listComments reads every page of comments, following the next links.
func (c *Client) listComments(ctx context.Context, path, operation string) ([]commentResponse, error) {
	comments := []commentResponse{}
	for path != "" {
		var payload commentPage
		if err := c.sendJSON(ctx, http.MethodGet, path, operation, nil, &payload); err != nil {
			return nil, err
		}
		comments = append(comments, payload.Results...)
		path = payload.Links.Next
		if path != "" && !strings.HasPrefix(path, "/wiki/") {
			path = "/wiki" + path
		}
	}
	return comments, nil
}

var (
	storageBreak = regexp.MustCompile(`(?i)<br\s*/?>|</(p|li|h[1-6]|tr|div|pre)>`)
	storageTag   = regexp.MustCompile(`<[^>]*>`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
)

// StorageText turns a storage-format fragment, such as a comment body, into
// plain text, one line per paragraph or list item.
func StorageText(storage string) string {
	text := storageBreak.ReplaceAllString(storage, "\n")
	text = storageTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
