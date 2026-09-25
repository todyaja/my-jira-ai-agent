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
	return Issue{
		Key:            payload.Key,
		Summary:        payload.Fields.Summary,
		Description:    adfToPlainText(payload.Fields.Description),
		DescriptionADF: payload.Fields.Description,
	}, nil
}

func (c *Client) UpdateDescription(ctx context.Context, key, description string) error {
	body, err := json.Marshal(struct {
		Description adfDocument `json:"description"`
	}{Description: plainTextToADF(description)})
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
		if transition.Name == statusName {
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
