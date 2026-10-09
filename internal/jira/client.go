package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	BaseURL    string
	Email      string
	Token      string
	HTTPClient *http.Client
}

type Issue struct {
	Key            string
	Summary        string
	Description    string
	DescriptionADF json.RawMessage
}

type issueResponse struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
	} `json:"fields"`
}

type transitionsResponse struct {
	Transitions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"transitions"`
}

func NewClient(baseURL, email, token string, httpClient *http.Client) *Client {
	return &Client{BaseURL: baseURL, Email: email, Token: token, HTTPClient: httpClient}
}

func (c *Client) GetIssue(ctx context.Context, key string) (Issue, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key), nil)
	if err != nil {
		return Issue{}, fmt.Errorf("GET issue: %w", err)
	}
	response, err := c.do(request, "GET issue")
	if err != nil {
		return Issue{}, err
	}
	defer response.Body.Close()

	var payload issueResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Issue{}, fmt.Errorf("GET issue: decode response: %w", err)
	}
	description, err := adfToPlainText(payload.Fields.Description)
	if err != nil {
		return Issue{}, fmt.Errorf("GET issue: convert description: %w", err)
	}
	return Issue{
		Key:            payload.Key,
		Summary:        payload.Fields.Summary,
		Description:    description,
		DescriptionADF: payload.Fields.Description,
	}, nil
}

func (c *Client) UpdateDescription(ctx context.Context, key string, description json.RawMessage) error {
	var payload struct {
		Fields struct {
			Description json.RawMessage `json:"description"`
		} `json:"fields"`
	}
	payload.Fields.Description = description
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("PUT description: encode request: %w", err)
	}
	request, err := c.newRequest(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(key), strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("PUT description: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request, "PUT description")
	if response != nil {
		response.Body.Close()
	}
	return err
}

func (c *Client) TransitionTo(ctx context.Context, key, statusName string) error {
	request, err := c.newRequest(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil)
	if err != nil {
		return fmt.Errorf("GET transitions: %w", err)
	}
	response, err := c.do(request, "GET transitions")
	if err != nil {
		return err
	}

	var payload transitionsResponse
	err = json.NewDecoder(response.Body).Decode(&payload)
	response.Body.Close()
	if err != nil {
		return fmt.Errorf("GET transitions: decode response: %w", err)
	}
	transitionID := ""
	for _, transition := range payload.Transitions {
		if strings.EqualFold(transition.Name, statusName) {
			transitionID = transition.ID
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("transition %q not found", statusName)
	}

	body, err := json.Marshal(struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
	}{Transition: struct {
		ID string `json:"id"`
	}{ID: transitionID}})
	if err != nil {
		return fmt.Errorf("POST transition: encode request: %w", err)
	}
	request, err = c.newRequest(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("POST transition: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = c.do(request, "POST transition")
	if response != nil {
		response.Body.Close()
	}
	return err
}

// GetIssueStatus returns the name of an issue's current status. Unlike
// search, it always reflects the latest change.
func (c *Client) GetIssueStatus(ctx context.Context, key string) (string, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"?fields=status", nil)
	if err != nil {
		return "", fmt.Errorf("GET issue status: %w", err)
	}
	response, err := c.do(request, "GET issue status")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("GET issue status: decode response: %w", err)
	}
	return payload.Fields.Status.Name, nil
}

type searchRequest struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

type searchResponse struct {
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	} `json:"issues"`
	NextPageToken string `json:"nextPageToken"`
}

// IssueStatus is an issue key with the name of its current Jira status.
type IssueStatus struct {
	Key    string
	Status string
}

// SearchIssues returns every issue matching jql with its current status,
// following pagination until Jira reports no further pages.
func (c *Client) SearchIssues(ctx context.Context, jql string) ([]IssueStatus, error) {
	issues := []IssueStatus{}
	nextPageToken := ""
	for {
		body, err := json.Marshal(searchRequest{JQL: jql, Fields: []string{"status"}, MaxResults: 50, NextPageToken: nextPageToken})
		if err != nil {
			return nil, fmt.Errorf("POST search: encode request: %w", err)
		}
		request, err := c.newRequest(ctx, http.MethodPost, "/rest/api/3/search/jql", strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("POST search: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := c.do(request, "POST search")
		if err != nil {
			return nil, err
		}

		var payload searchResponse
		err = json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("POST search: decode response: %w", err)
		}
		for _, issue := range payload.Issues {
			issues = append(issues, IssueStatus{Key: issue.Key, Status: issue.Fields.Status.Name})
		}
		if payload.NextPageToken == "" {
			return issues, nil
		}
		nextPageToken = payload.NextPageToken
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.Email, c.Token)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func (c *Client) do(request *http.Request, operation string) (*http.Response, error) {
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, fmt.Errorf("%s: Jira returned HTTP %d", operation, response.StatusCode)
	}
	return response, nil
}
