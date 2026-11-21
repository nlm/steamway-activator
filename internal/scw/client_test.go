package scw

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "fr-par-2", "abc", func(context.Context) (string, error) { return "key", nil })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewBadZone(t *testing.T) {
	if _, err := New("", "mars-1", "abc", func(context.Context) (string, error) { return "", nil }); err == nil {
		t.Error("New accepted an unknown zone")
	}
}

func TestState(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/instance/v1/zones/fr-par-2/servers/abc" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Auth-Token"); got != "key" {
			t.Errorf("X-Auth-Token = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"server":{"id":"abc","state":"stopped in place"}}`)
	})
	got, err := c.State(context.Background())
	if err != nil || got != "stopped in place" {
		t.Fatalf("State = %q, %v", got, err)
	}
}

func testAction(t *testing.T, name, body string, call func(*Client, context.Context) error) {
	t.Run(name, func(t *testing.T) {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/instance/v1/zones/fr-par-2/servers/abc/action" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := r.Header.Get("X-Auth-Token"); got != "key" {
				t.Errorf("X-Auth-Token = %q", got)
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != body {
				t.Errorf("body = %s", b)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"task":{"id":"t1","status":"pending"}}`)
		})
		if err := call(c, context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPowerOn(t *testing.T) {
	testAction(t, "poweron", `{"action":"poweron"}`, (*Client).PowerOn)
}

func TestPowerOff(t *testing.T) {
	testAction(t, "poweroff", `{"action":"poweroff"}`, (*Client).PowerOff)
}

func TestAPIError(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(412)
		io.WriteString(w, `{"message":"Not enough resources","type":"out_of_stock"}`)
	})
	err := c.PowerOn(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.Status != 412 || ae.Message != "Not enough resources" {
		t.Errorf("got %+v", ae)
	}
	if IsAuth(err) {
		t.Error("IsAuth(412) = true")
	}
}

func TestAPIErrorFallbackMessage(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		io.WriteString(w, "<html>bad gateway</html>")
	})
	_, err := c.State(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Message != http.StatusText(502) {
		t.Fatalf("err = %v", err)
	}
}

func TestTypedAuthErrors(t *testing.T) {
	for status, body := range map[int]string{
		401: `{"message":"denied authentication","type":"denied_authentication","method":"api_key","reason":"not_found"}`,
		403: `{"message":"insufficient permissions","type":"permissions_denied","details":[{"resource":"servers","action":"read"}]}`,
	} {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
		})
		_, err := c.State(context.Background())
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != status || ae.Message == "" || !IsAuth(err) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestState_EmptyIsError(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"server":{"id":"abc"}}`)
	})
	if got, err := c.State(context.Background()); err == nil {
		t.Errorf("State = %q, nil error", got)
	}
}

func TestIsAuth(t *testing.T) {
	for status, want := range map[int]bool{401: true, 403: true, 500: false, 429: false} {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		_, err := c.State(context.Background())
		if err == nil {
			t.Fatalf("status %d: nil error", status)
		}
		if got := IsAuth(err); got != want {
			t.Errorf("IsAuth(%d) = %v, want %v", status, got, want)
		}
	}
	if IsAuth(errors.New("x")) || IsAuth(nil) {
		t.Error("IsAuth true for non-API error")
	}
}

func TestKeyError(t *testing.T) {
	called := false
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	c.Key = func(context.Context) (string, error) { return "", errors.New("locked") }
	if _, err := c.State(context.Background()); !errors.Is(err, ErrKey) {
		t.Errorf("State err = %v", err)
	}
	if err := c.PowerOff(context.Background()); !errors.Is(err, ErrKey) {
		t.Errorf("PowerOff err = %v", err)
	}
	if called {
		t.Error("HTTP request made despite key error")
	}
}

func TestNewNilKey(t *testing.T) {
	if _, err := New("", "fr-par-2", "abc", nil); err == nil {
		t.Error("New accepted a nil key function")
	}
}

func TestNotFound(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		io.WriteString(w, `{"type":"not_found","resource":"instance_server","resource_id":"abc","message":"resource is not found"}`)
	})
	for name, err := range map[string]error{
		"State":    func() error { _, err := c.State(context.Background()); return err }(),
		"PowerOff": c.PowerOff(context.Background()),
	} {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 404 || ae.Message != "resource is not found" {
			t.Errorf("%s: err = %v", name, err)
		}
		if !IsNotFound(err) || IsAuth(err) {
			t.Errorf("%s: IsNotFound=%v IsAuth=%v", name, IsNotFound(err), IsAuth(err))
		}
	}
	if IsNotFound(errors.New("x")) || IsNotFound(nil) {
		t.Error("IsNotFound true for non-API error")
	}
}
