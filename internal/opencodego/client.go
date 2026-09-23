package opencodego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

const remoteSource primitives.SourceID = "llm.opencodego"

// DefaultBaseURL is the OpenCode Go gateway root; the chat-completions path
// is appended to it.
const DefaultBaseURL = "https://opencode.ai/zen/go/v1"

// DefaultMaxAttempts mirrors the harness's remote retry default.
const DefaultMaxAttempts = primitives.DefaultRemoteMaxAttempts

// APIError carries a gateway error response.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *APIError) Error() string {
	if err.Code != "" {
		return fmt.Sprintf("opencode-go error %s: %s", err.Code, err.Message)
	}
	if err.StatusCode != 0 {
		return fmt.Sprintf("opencode-go request failed with status %d: %s", err.StatusCode, err.Message)
	}
	return "opencode-go request failed: " + err.Message
}

// Exchange borrows read-only bodies for tracing: request JSON, terminal
// response JSON, or an HTTP error body.
type Exchange struct {
	RequestBody  []byte
	RequestURL   string
	RequestHead  http.Header
	StatusCode   int
	ResponseBody []byte
}

type Config struct {
	APIKey      string
	BaseURL     string
	MaxAttempts *int
	// Trace, when set, borrows read-only exchange bodies.
	Trace func(Exchange)
}

// Client implements llm.Adapter against the OpenCode Go chat-completions
// endpoint. The session id arrives after construction (the runner opens the
// session after building its clients), so SetSessionID must be called before
// the first turn; the gateway answers HTTP 400 MissingSessionID without it.
type Client struct {
	remote *primitives.RemoteClient
	config Config

	endpoint    string
	maxAttempts int

	mu        sync.RWMutex
	sessionID string
}

var _ llm.Adapter = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("OpenCode Go API key must be set")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	maxAttempts := DefaultMaxAttempts
	if config.MaxAttempts != nil {
		maxAttempts = *config.MaxAttempts
	}
	if maxAttempts <= 0 {
		return nil, errors.New("max attempts must be positive")
	}
	return &Client{
		remote:      primitives.NewRemoteClient(),
		config:      config,
		endpoint:    baseURL + chatPath,
		maxAttempts: maxAttempts,
	}, nil
}

// SetSessionID attaches zua's session id; every subsequent request carries it
// in the mandatory x-opencode-session header.
func (client *Client) SetSessionID(sessionID string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.sessionID = sessionID
}

// Close releases the underlying remote client's connections.
func (client *Client) Close() error {
	return client.remote.Close()
}

// Respond runs one chat-completions exchange for the harness request. The
// session id must already be set (SetSessionID after the session opens): the
// gateway answers HTTP 400 MissingSessionID without it, so fail fast instead
// of paying a doomed network round trip.
func (client *Client) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	client.mu.RLock()
	sessionID := client.sessionID
	client.mu.RUnlock()
	if sessionID == "" {
		return llm.Response{}, errors.New("opencode-go session id not set: attach zua's session id with SetSessionID before the first turn")
	}

	body, err := json.Marshal(buildChatRequest(request))
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode chat request: %w", err)
	}

	remoteRequest := primitives.DefaultRemoteRequest(remoteSource, primitives.CorrelationID(uuid.New().String()), client.endpoint)
	remoteRequest.Method = http.MethodPost
	remoteRequest.Body = body
	remoteRequest.Headers = map[string][]string{
		"Authorization": {"Bearer " + client.config.APIKey},
		"Content-Type":  {"application/json"},
		"Accept":        {"application/json"},
		SessionHeader:   {sessionID},
	}
	remoteRequest.RetryPolicy.MaxAttempts = client.maxAttempts
	// Reasoning can produce multi-minute gaps.
	remoteRequest.ResponseIdleTimeout = 30 * time.Minute

	status, responseBody, err := client.exchange(ctx, remoteRequest)
	if client.config.Trace != nil {
		client.config.Trace(Exchange{
			RequestBody: body, RequestURL: remoteRequest.URL, RequestHead: tracedHead(remoteRequest.Headers),
			StatusCode: status, ResponseBody: responseBody,
		})
	}
	if err != nil {
		return llm.Response{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return llm.Response{}, fmt.Errorf("chat completion: %w", gatewayError(status, responseBody))
	}
	return decodeChatResponse(responseBody)
}

// tracedHead copies the request headers for tracing with the Authorization
// value scrubbed — trace sinks may log or persist the exchange.
func tracedHead(headers map[string][]string) http.Header {
	head := make(http.Header, len(headers)+1)
	for name, values := range headers {
		head[name] = values
	}
	if head.Get("Authorization") != "" {
		head.Set("Authorization", "Bearer [scrubbed]")
	}
	return head
}

// exchange runs the remote request and reassembles the plain-JSON response
// body from the remote client's output events. A retryable attempt streams
// its (error) body before the retry, so per-attempt state resets whenever a
// new attempt number appears.
func (client *Client) exchange(ctx context.Context, remoteRequest primitives.RemoteRequest) (int, []byte, error) {
	events := make(chan primitives.PrimitiveEvent)
	client.remote.SendRequest(ctx, remoteRequest, events)
	attempt := 0
	var statusCode int
	var body []byte
	for {
		event := <-events
		switch event.Type {
		case primitives.PrimitiveEventRemoteResponseStarted:
			started := event.Result.(primitives.RemoteResponseStartedResult)
			if started.Attempt != attempt {
				attempt = started.Attempt
				statusCode = 0
				body = nil
			}
			statusCode = started.StatusCode
		case primitives.PrimitiveEventRemoteOutput:
			output := event.Result.(primitives.RemoteOutputResult)
			if output.Attempt != attempt {
				attempt = output.Attempt
				statusCode = 0
				body = nil
			}
			if len(body)+len(output.Data) > maxResponseBodyBytes {
				return statusCode, nil, errors.New("opencode-go response exceeds 4 MiB")
			}
			body = append(body, output.Data...)
		case primitives.PrimitiveEventRemoteCompleted:
			return statusCode, body, nil
		case primitives.PrimitiveEventFailed:
			if ctx.Err() != nil {
				return statusCode, nil, ctx.Err()
			}
			failure, ok := event.Result.(primitives.PrimitiveFailureResult)
			if !ok {
				return statusCode, nil, errors.New("remote request failed with an invalid result")
			}
			return statusCode, nil, errors.New(failure.Error)
		case primitives.PrimitiveEventCanceled:
			if ctx.Err() != nil {
				return statusCode, nil, ctx.Err()
			}
			return statusCode, nil, context.Canceled
		}
	}
}

// gatewayError decodes the gateway's JSON error envelope, falling back to the
// raw body as the message.
func gatewayError(statusCode int, body []byte) *APIError {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Error.Message != "" || envelope.Error.Code != "") {
		return &APIError{StatusCode: statusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &APIError{StatusCode: statusCode, Message: message}
}

const maxResponseBodyBytes = 4 << 20
