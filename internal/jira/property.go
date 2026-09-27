package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GetIssueProperty returns the JSON value stored on an issue under
// propertyKey, and whether it is set.
func (c *Client) GetIssueProperty(ctx context.Context, key, propertyKey string) (json.RawMessage, bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, issuePropertyPath(key, propertyKey), nil)
	if err != nil {
		return nil, false, fmt.Errorf("GET issue property: %w", err)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, false, fmt.Errorf("GET issue property: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, false, fmt.Errorf("GET issue property: Jira returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, false, fmt.Errorf("GET issue property: decode response: %w", err)
	}
	return payload.Value, true, nil
}

// SetIssueProperty stores a JSON value on an issue under propertyKey,
// replacing any previous value.
func (c *Client) SetIssueProperty(ctx context.Context, key, propertyKey string, value json.RawMessage) error {
	request, err := c.newRequest(ctx, http.MethodPut, issuePropertyPath(key, propertyKey), strings.NewReader(string(value)))
	if err != nil {
		return fmt.Errorf("PUT issue property: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request, "PUT issue property")
	if response != nil {
		response.Body.Close()
	}
	return err
}

func issuePropertyPath(key, propertyKey string) string {
	return "/rest/api/3/issue/" + url.PathEscape(key) + "/properties/" + url.PathEscape(propertyKey)
}
