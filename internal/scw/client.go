// Package scw reads a Scaleway Instance server's state and sends power
// actions, using the official Scaleway Go SDK.
package scw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	sdk "github.com/scaleway/scaleway-sdk-go/scw"
)

// ErrKey wraps any failure to obtain the secret key.
var ErrKey = errors.New("scaleway secret key unavailable")

// APIError is an error answer from the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("scaleway API: %d %s", e.Status, e.Message)
}

// IsAuth reports whether err is an API 401 or 403 response.
func IsAuth(err error) bool {
	ae, ok := errors.AsType[*APIError](err)
	return ok && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

// IsNotFound reports whether err is an API 404 response (wrong server ID,
// deleted server).
func IsNotFound(err error) bool {
	ae, ok := errors.AsType[*APIError](err)
	return ok && ae.Status == http.StatusNotFound
}

// Client talks to one server in one zone. The secret key is read through Key
// on every call and handed to the SDK per request (sdk.WithAuthRequest), so
// the SDK client holds no credentials.
type Client struct {
	Zone     sdk.Zone
	ServerID string
	Key      func(ctx context.Context) (string, error)

	api *instance.API
}

// New builds a Client. apiURL may be empty (production API); tests pass the
// URL of a fake server. The zone must be well formed.
func New(apiURL, zone, serverID string, key func(ctx context.Context) (string, error)) (*Client, error) {
	if key == nil {
		return nil, errors.New("scw: nil key function")
	}
	z, err := sdk.ParseZone(zone)
	if err != nil {
		return nil, fmt.Errorf("zone %q: %w", zone, err)
	}
	opts := []sdk.ClientOption{sdk.WithHTTPClient(&http.Client{Timeout: 10 * time.Second})}
	if apiURL != "" {
		opts = append(opts, sdk.WithAPIURL(apiURL))
	}
	cl, err := sdk.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &Client{Zone: z, ServerID: serverID, Key: key, api: instance.NewAPI(cl)}, nil
}

// State returns the server's state, e.g. "running" or "stopped in place".
func (c *Client) State(ctx context.Context) (string, error) {
	opts, err := c.requestOptions(ctx)
	if err != nil {
		return "", err
	}
	resp, err := c.api.GetServer(&instance.GetServerRequest{Zone: c.Zone, ServerID: c.ServerID}, opts...)
	if err != nil {
		return "", convertError(err)
	}
	if resp.Server == nil || resp.Server.State == "" {
		return "", errors.New("scaleway API: answer without server state")
	}
	return string(resp.Server.State), nil
}

// PowerOn sends the "poweron" action.
func (c *Client) PowerOn(ctx context.Context) error {
	return c.action(ctx, instance.ServerActionPoweron)
}

// PowerOff sends the "poweroff" action (never stop_in_place).
func (c *Client) PowerOff(ctx context.Context) error {
	return c.action(ctx, instance.ServerActionPoweroff)
}

func (c *Client) action(ctx context.Context, action instance.ServerAction) error {
	opts, err := c.requestOptions(ctx)
	if err != nil {
		return err
	}
	_, err = c.api.ServerAction(&instance.ServerActionRequest{Zone: c.Zone, ServerID: c.ServerID, Action: action}, opts...)
	return convertError(err)
}

// requestOptions reads the key and returns the per-request SDK options.
// The access key is not needed: the API authenticates with X-Auth-Token only,
// and the SDK validates the access key only for client-level credentials.
func (c *Client) requestOptions(ctx context.Context) ([]sdk.RequestOption, error) {
	key, err := c.Key(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return []sdk.RequestOption{sdk.WithContext(ctx), sdk.WithAuthRequest("", key)}, nil
}

// convertError turns SDK errors that carry (or imply) an HTTP status into
// *APIError. Other errors (transport, timeout, decoding) pass through.
func convertError(err error) error {
	if err == nil {
		return nil
	}
	if re, ok := errors.AsType[*sdk.ResponseError](err); ok {
		msg := re.Message
		if msg == "" || msg == re.Status { // the SDK puts the status line there for non-JSON bodies
			msg = http.StatusText(re.StatusCode)
		}
		return &APIError{Status: re.StatusCode, Message: msg}
	}
	// Typed errors drop the HTTP status; the API uses a fixed one per type.
	if e, ok := errors.AsType[*sdk.DeniedAuthenticationError](err); ok {
		return &APIError{Status: http.StatusUnauthorized, Message: rawMessage(e.RawBody, err)}
	}
	if e, ok := errors.AsType[*sdk.PermissionsDeniedError](err); ok {
		return &APIError{Status: http.StatusForbidden, Message: rawMessage(e.RawBody, err)}
	}
	if e, ok := errors.AsType[*sdk.ResourceNotFoundError](err); ok {
		return &APIError{Status: http.StatusNotFound, Message: rawMessage(e.RawBody, err)}
	}
	if e, ok := errors.AsType[*sdk.OutOfStockError](err); ok {
		return &APIError{Status: http.StatusPreconditionFailed, Message: rawMessage(e.RawBody, err)}
	}
	return err
}

// rawMessage returns the "message" of an API error body, else err's text.
func rawMessage(body json.RawMessage, err error) string {
	var b struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &b) == nil && b.Message != "" {
		return b.Message
	}
	return err.Error()
}
