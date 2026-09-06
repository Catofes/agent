package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const defaultMaxResponseBytes = 512 << 10

var ErrUnavailable = errors.New("python runner unavailable")

type Client struct {
	BaseURL          string
	Token            string
	HTTP             *http.Client
	MaxResponseBytes int64
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: httpClient, MaxResponseBytes: defaultMaxResponseBytes}
}

func (c *Client) Execute(ctx context.Context, request ExecuteRequest) (ExecuteResponse, error) {
	var response ExecuteResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/executions", request, &response); err != nil {
		return ExecuteResponse{}, err
	}
	return response, nil
}

func (c *Client) Upload(ctx context.Context, name string, source io.Reader) (Artifact, error) {
	reader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()
	go func() {
		part, err := writer.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, source)
		}
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		_ = pipeWriter.CloseWithError(err)
	}()
	defer reader.Close()
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/artifacts", reader)
	if err != nil {
		return Artifact{}, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http().Do(req)
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Artifact{}, decodeError(resp)
	}
	var artifact Artifact
	if err = decodeLimited(resp.Body, c.responseLimit(), &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode runner upload response: %w", err)
	}
	return artifact, nil
}

func (c *Client) Download(ctx context.Context, id string) (*http.Response, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	return resp, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/artifacts/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return decodeError(resp)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if err = decodeLimited(resp.Body, c.responseLimit(), output); err != nil {
		return fmt.Errorf("decode runner response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid runner URL")
	}
	base.Path = path.Join(base.Path, endpoint)
	req, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) responseLimit() int64 {
	if c.MaxResponseBytes > 0 {
		return c.MaxResponseBytes
	}
	return defaultMaxResponseBytes
}

func decodeError(resp *http.Response) error {
	var payload ErrorResponse
	if err := decodeLimited(resp.Body, 64<<10, &payload); err == nil && payload.Error.Message != "" {
		return fmt.Errorf("runner %s: %s", payload.Error.Code, payload.Error.Message)
	}
	return fmt.Errorf("runner returned HTTP %d", resp.StatusCode)
}

func decodeLimited(reader io.Reader, limit int64, output any) error {
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("response exceeds %d bytes", limit)
	}
	if err = json.Unmarshal(data, output); err != nil {
		return err
	}
	return nil
}
