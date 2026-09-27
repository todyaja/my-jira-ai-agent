package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Comment is an issue comment as plain text, with the entity properties
// stored on it keyed by property name.
type Comment struct {
	ID              string
	Author          string
	AuthorAccountID string
	Created         string
	Body            string
	Properties      map[string]json.RawMessage
}

type commentsResponse struct {
	StartAt  int `json:"startAt"`
	Total    int `json:"total"`
	Comments []struct {
		ID     string `json:"id"`
		Author struct {
			AccountID   string `json:"accountId"`
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Created    string          `json:"created"`
		Body       json.RawMessage `json:"body"`
		Properties []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		} `json:"properties"`
	} `json:"comments"`
}

// GetComments returns every comment on an issue with its properties, oldest
// first, following pagination until all comments are read.
func (c *Client) GetComments(ctx context.Context, key string) ([]Comment, error) {
	comments := []Comment{}
	for {
		query := url.Values{"orderBy": {"created"}, "maxResults": {"100"}, "startAt": {strconv.Itoa(len(comments))}, "expand": {"properties"}}
		request, err := c.newRequest(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment?"+query.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("GET comments: %w", err)
		}
		response, err := c.do(request, "GET comments")
		if err != nil {
			return nil, err
		}

		var payload commentsResponse
		err = json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("GET comments: decode response: %w", err)
		}
		for _, comment := range payload.Comments {
			body, err := adfToPlainText(comment.Body)
			if err != nil {
				return nil, fmt.Errorf("GET comments: convert comment %s: %w", comment.ID, err)
			}
			properties := map[string]json.RawMessage{}
			for _, property := range comment.Properties {
				properties[property.Key] = property.Value
			}
			comments = append(comments, Comment{
				ID:              comment.ID,
				Author:          comment.Author.DisplayName,
				AuthorAccountID: comment.Author.AccountID,
				Created:         comment.Created,
				Body:            body,
				Properties:      properties,
			})
		}
		if len(payload.Comments) == 0 || len(comments) >= payload.Total {
			return comments, nil
		}
	}
}

// AddComment posts an ADF comment on an issue and returns its ID.
func (c *Client) AddComment(ctx context.Context, key string, body json.RawMessage) (string, error) {
	encoded, err := json.Marshal(struct {
		Body json.RawMessage `json:"body"`
	}{Body: body})
	if err != nil {
		return "", fmt.Errorf("POST comment: encode request: %w", err)
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", strings.NewReader(string(encoded)))
	if err != nil {
		return "", fmt.Errorf("POST comment: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request, "POST comment")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("POST comment: decode response: %w", err)
	}
	return created.ID, nil
}

// SetCommentProperty stores a JSON value on a comment under propertyKey,
// replacing any previous value.
func (c *Client) SetCommentProperty(ctx context.Context, commentID, propertyKey string, value json.RawMessage) error {
	path := "/rest/api/3/comment/" + url.PathEscape(commentID) + "/properties/" + url.PathEscape(propertyKey)
	request, err := c.newRequest(ctx, http.MethodPut, path, strings.NewReader(string(value)))
	if err != nil {
		return fmt.Errorf("PUT comment property: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request, "PUT comment property")
	if response != nil {
		response.Body.Close()
	}
	return err
}

// maxQuotedRunes bounds how much of the original comment a reply quotes.
const maxQuotedRunes = 300

// ReplyADF builds a comment that answers another one, since Jira comments
// cannot be threaded: it mentions the original author when their account is
// known, states message with a link, and quotes the original comment.
func ReplyADF(original Comment, message, linkText, href string) json.RawMessage {
	line := []json.RawMessage{}
	if original.AuthorAccountID != "" {
		line = append(line,
			mustJSON(map[string]any{"type": "mention", "attrs": map[string]string{"id": original.AuthorAccountID, "text": "@" + original.Author}}),
			mustJSON(adfNode{Type: "text", Text: " "}),
		)
	}
	line = append(line, mustJSON(adfNode{Type: "text", Text: message}))
	if href != "" {
		line = append(line,
			mustJSON(adfNode{Type: "text", Text: " "}),
			mustJSON(map[string]any{
				"type":  "text",
				"text":  linkText,
				"marks": []any{map[string]any{"type": "link", "attrs": map[string]string{"href": href}}},
			}),
		)
	}

	quoted := []rune(strings.TrimSpace(original.Body))
	if len(quoted) > maxQuotedRunes {
		quoted = append(quoted[:maxQuotedRunes], '…')
	}
	document := adfDocument{Type: "doc", Version: 1, Content: []json.RawMessage{
		mustJSON(adfNode{Type: "paragraph", Content: line}),
		mustJSON(adfNode{Type: "blockquote", Content: []json.RawMessage{textParagraph(string(quoted))}}),
	}}
	return mustJSON(document)
}

// TextADF builds a single-paragraph comment.
func TextADF(text string) json.RawMessage {
	return mustJSON(adfDocument{Type: "doc", Version: 1, Content: []json.RawMessage{textParagraph(text)}})
}
