package cloudcode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DailyEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	ProdEndpoint  = "https://cloudcode-pa.googleapis.com"

	PathLoadCodeAssist       = "/v1internal:loadCodeAssist"
	PathOnboardUser          = "/v1internal:onboardUser"
	PathFetchAvailableModels = "/v1internal:fetchAvailableModels"
	PathGenerateContent      = "/v1internal:generateContent"
	PathStreamGenerate       = "/v1internal:streamGenerateContent?alt=sse"

	DefaultUserAgentVersion = "2.0.3"
	DefaultClientVersion    = "1.110.0"
	GoogleAPIClient         = "gl-go/1.26.4 auth/0.5 google-api-go-client/0.5"
)

var (
	ContentEndpoints      = []string{DailyEndpoint, ProdEndpoint}
	ProvisioningEndpoints = []string{DailyEndpoint, ProdEndpoint}
)

type Options struct {
	AccessToken      string
	UserAgentVersion string
	ClientVersion    string
	Timeout          time.Duration
	HTTPClient       *http.Client
}

// Client implements the HTTPS JSON/SSE transport used by current agy.
type Client struct {
	httpClient            *http.Client
	transport             *http.Transport
	accessToken           string
	userAgent             string
	clientVersion         string
	contentEndpoints      []string
	provisioningEndpoints []string
	defaultHeader         http.Header
}

type ClientMetadata struct {
	IdeType       int    `json:"ideType"`
	IdeVersion    string `json:"ideVersion,omitempty"`
	PluginVersion string `json:"pluginVersion,omitempty"`
	Platform      int    `json:"platform"`
	UpdateChannel string `json:"updateChannel,omitempty"`
	DuetProject   string `json:"duetProject,omitempty"`
	PluginType    int    `json:"pluginType"`
	IdeName       string `json:"ideName,omitempty"`
}

type LoadCodeAssistRequest struct {
	Metadata ClientMetadata `json:"metadata"`
	Mode     int            `json:"mode"`
}

type OnboardUserRequest struct {
	TierID   string         `json:"tierId"`
	Metadata ClientMetadata `json:"metadata"`
}

type Response struct {
	Endpoint   string
	StatusCode int
	Header     http.Header
	Body       []byte
}

type HTTPError struct {
	Endpoint   string
	StatusCode int
	Status     string
	Body       string
	Header     http.Header
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Cloud Code request to %s failed (%s): %s", e.Endpoint, e.Status, e.Body)
}

type RequestOptions struct {
	Accept    string
	SessionID string
	Headers   http.Header
}

type SSEEvent struct {
	Event string
	Data  []byte
	ID    string
	Retry time.Duration
}

func New(options Options) *Client {
	if options.UserAgentVersion == "" {
		options.UserAgentVersion = DefaultUserAgentVersion
	}
	if options.ClientVersion == "" {
		options.ClientVersion = DefaultClientVersion
	}

	var transport *http.Transport
	client := options.HTTPClient
	if client == nil {
		// Current agy's Cloud Code ClientHello has no ALPN. A dedicated standard
		// Transport with only an empty TLS config reproduces that behavior. Do
		// not set ForceAttemptHTTP2 or any field inside tls.Config.
		transport = &http.Transport{
			TLSClientConfig:     &tls.Config{},
			MaxIdleConnsPerHost: 10,
			MaxIdleConns:        20,
			IdleConnTimeout:     90 * time.Second,
		}
		client = &http.Client{Transport: transport, Timeout: options.Timeout}
	}

	userAgent := platformUserAgent(options.UserAgentVersion)
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+options.AccessToken)
	header.Set("Content-Type", "application/json")
	header.Set("Accept-Encoding", "identity")
	header.Set("User-Agent", userAgent)
	header.Set("X-Client-Name", "antigravity")
	header.Set("X-Client-Version", options.ClientVersion)
	header.Set("x-goog-api-client", GoogleAPIClient)

	return &Client{
		httpClient:            client,
		transport:             transport,
		accessToken:           options.AccessToken,
		userAgent:             userAgent,
		clientVersion:         options.ClientVersion,
		contentEndpoints:      append([]string(nil), ContentEndpoints...),
		provisioningEndpoints: append([]string(nil), ProvisioningEndpoints...),
		defaultHeader:         header,
	}
}

func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

func Metadata(projectID string) ClientMetadata {
	return ClientMetadata{
		IdeType:     9,
		Platform:    platformEnum(),
		DuetProject: projectID,
		PluginType:  2,
	}
}

func (c *Client) LoadCodeAssist(ctx context.Context, projectID string) (Response, error) {
	request := LoadCodeAssistRequest{Metadata: Metadata(projectID), Mode: 1}
	return c.DoJSON(ctx, c.provisioningEndpoints, PathLoadCodeAssist, request, RequestOptions{})
}

func (c *Client) OnboardUser(ctx context.Context, tierID, projectID string) (Response, error) {
	request := OnboardUserRequest{TierID: tierID, Metadata: Metadata(projectID)}
	return c.DoJSON(ctx, c.contentEndpoints, PathOnboardUser, request, RequestOptions{})
}

func (c *Client) FetchAvailableModels(ctx context.Context, projectID string) (Response, error) {
	request := map[string]string{}
	if projectID != "" {
		request["project"] = projectID
	}
	return c.DoJSON(ctx, c.contentEndpoints, PathFetchAvailableModels, request, RequestOptions{})
}

func (c *Client) GenerateContent(ctx context.Context, payload any, options RequestOptions) (Response, error) {
	return c.DoJSON(ctx, c.contentEndpoints, PathGenerateContent, payload, options)
}

func (c *Client) StreamGenerateContent(ctx context.Context, payload any, options RequestOptions, consume func(SSEEvent) error) (Response, error) {
	options.Accept = "text/event-stream"
	return c.DoSSE(ctx, c.contentEndpoints, PathStreamGenerate, payload, options, consume)
}

func (c *Client) DoJSON(ctx context.Context, endpoints []string, path string, payload any, options RequestOptions) (Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("encode Cloud Code request: %w", err)
	}
	var failures []error
	for _, endpoint := range endpoints {
		request, err := c.newRequest(ctx, endpoint, path, body, options)
		if err != nil {
			return Response{}, err
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			failures = append(failures, fmt.Errorf("Cloud Code request to %s: %w", endpoint, err))
			continue
		}
		result, responseErr := readResponse(endpoint, response)
		if responseErr != nil {
			failures = append(failures, responseErr)
			continue
		}
		return result, nil
	}
	return Response{}, errors.Join(failures...)
}

func (c *Client) DoSSE(ctx context.Context, endpoints []string, path string, payload any, options RequestOptions, consume func(SSEEvent) error) (Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("encode Cloud Code streaming request: %w", err)
	}
	var failures []error
	for _, endpoint := range endpoints {
		request, err := c.newRequest(ctx, endpoint, path, body, options)
		if err != nil {
			return Response{}, err
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			failures = append(failures, fmt.Errorf("Cloud Code stream to %s: %w", endpoint, err))
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			result, responseErr := readResponse(endpoint, response)
			if responseErr != nil {
				failures = append(failures, responseErr)
			} else {
				failures = append(failures, fmt.Errorf("unexpected streaming response: %#v", result))
			}
			continue
		}
		result := Response{Endpoint: endpoint, StatusCode: response.StatusCode, Header: response.Header}
		err = ParseSSE(response.Body, consume)
		closeErr := response.Body.Close()
		if err != nil {
			return result, fmt.Errorf("parse Cloud Code SSE stream: %w", err)
		}
		if closeErr != nil {
			return result, fmt.Errorf("close Cloud Code SSE stream: %w", closeErr)
		}
		return result, nil
	}
	return Response{}, errors.Join(failures...)
}

func (c *Client) newRequest(ctx context.Context, endpoint, path string, body []byte, options RequestOptions) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Cloud Code request: %w", err)
	}
	request.Header = c.defaultHeader.Clone()
	if options.Accept != "" && options.Accept != "application/json" {
		request.Header.Set("Accept", options.Accept)
	}
	if options.SessionID != "" {
		request.Header.Set("X-Machine-Session-Id", options.SessionID)
	}
	for name, values := range options.Headers {
		request.Header.Del(name)
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return request, nil
}

func readResponse(endpoint string, response *http.Response) (Response, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return Response{}, fmt.Errorf("read Cloud Code response from %s: %w", endpoint, err)
	}
	result := Response{
		Endpoint:   endpoint,
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       body,
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &HTTPError{
			Endpoint:   endpoint,
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Body:       strings.TrimSpace(string(body)),
			Header:     response.Header,
		}
	}
	return result, nil
}

// ParseSSE implements the event-stream field and multi-line data rules used by
// agy. A final unterminated event is dispatched at EOF.
func ParseSSE(reader io.Reader, consume func(SSEEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var event SSEEvent
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event.Event = ""
			event.Retry = 0
			return nil
		}
		if len(data) == 1 {
			event.Data = []byte(data[0])
		} else {
			event.Data = []byte(strings.Join(data, "\n"))
		}
		if consume != nil {
			if err := consume(event); err != nil {
				return err
			}
		}
		event.Event = ""
		event.Data = nil
		event.Retry = 0
		data = data[:0]
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				event.ID = value
			}
		case "retry":
			milliseconds, err := strconv.ParseInt(value, 10, 64)
			if err == nil && milliseconds >= 0 {
				event.Retry = time.Duration(milliseconds) * time.Millisecond
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

func platformUserAgent(version string) string {
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	return fmt.Sprintf("antigravity/%s %s/%s", version, osName, runtime.GOARCH)
}

func platformEnum() int {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		return 1
	case "darwin/arm64":
		return 2
	case "linux/amd64":
		return 3
	case "linux/arm64":
		return 4
	case "windows/amd64":
		return 5
	default:
		return 0
	}
}
