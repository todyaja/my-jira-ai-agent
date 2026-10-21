package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CheckedOptions returns the selected options of a checkbox or multi-select
// field, in order, or none when the field is empty or not on the issue.
func (i Issue) CheckedOptions(fieldID string) ([]string, error) {
	raw := bytes.TrimSpace(i.Fields[fieldID])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return []string{}, nil
	}
	var options []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, fmt.Errorf("field %s is not a checkbox field: %w", fieldID, err)
	}
	checked := []string{}
	for _, option := range options {
		if value := strings.TrimSpace(option.Value); value != "" {
			checked = append(checked, value)
		}
	}
	return checked, nil
}

type fieldResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope *struct {
		Type    string `json:"type"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	} `json:"scope"`
}

// FindFieldID returns the id of the field named name, ignoring case, used by
// the project with projectKey. A team-managed project's own field wins over
// a site-wide one with the same name. It reports false when there is none.
func (c *Client) FindFieldID(ctx context.Context, name, projectKey string) (string, bool, error) {
	var project struct {
		ID string `json:"id"`
	}
	if err := c.getJSON(ctx, "/rest/api/3/project/"+url.PathEscape(projectKey), "GET project", &project); err != nil {
		return "", false, err
	}
	var fields []fieldResponse
	if err := c.getJSON(ctx, "/rest/api/3/field", "GET fields", &fields); err != nil {
		return "", false, err
	}
	global := ""
	for _, field := range fields {
		if !strings.EqualFold(strings.TrimSpace(field.Name), strings.TrimSpace(name)) {
			continue
		}
		if field.Scope == nil {
			if global == "" {
				global = field.ID
			}
			continue
		}
		if field.Scope.Project.ID == project.ID {
			return field.ID, true, nil
		}
	}
	return global, global != "", nil
}

func (c *Client) getJSON(ctx context.Context, path, operation string, target any) error {
	request, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	response, err := c.do(request, operation)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("%s: decode response: %w", operation, err)
	}
	return nil
}
