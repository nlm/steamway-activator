package sunshine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestCurrentGame_Running(t *testing.T) {
	c := serve(t, 200, `<?xml version="1.0" encoding="utf-8"?><root status_code="200"><hostname>gaming-host</hostname><currentgame>881448767</currentgame><state>SUNSHINE_SERVER_BUSY</state></root>`)
	got, err := c.CurrentGame(context.Background())
	if err != nil || got != 881448767 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestCurrentGame_None(t *testing.T) {
	c := serve(t, 200, `<?xml version="1.0" encoding="utf-8"?><root status_code="200"><hostname>gaming-host</hostname><currentgame>0</currentgame><state>SUNSHINE_SERVER_FREE</state></root>`)
	got, err := c.CurrentGame(context.Background())
	if err != nil || got != 0 {
		t.Fatalf("got %d, %v", got, err)
	}
}

func TestCurrentGame_Junk(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"html":          {200, `<html>502 Bad Gateway</html>`},
		"missing":       {200, `<root><hostname>x</hostname></root>`},
		"non-integer":   {200, `<root><currentgame>abc</currentgame></root>`},
		"empty element": {200, `<root><currentgame></currentgame></root>`},
		"self-closing":  {200, `<root><currentgame/></root>`},
		"whitespace":    {200, `<root><currentgame>   </currentgame></root>`},
		"newline only":  {200, "<root><currentgame>\n\t</currentgame></root>"},
		"wrong root":    {200, `<html><currentgame>0</currentgame></html>`},
		"empty body":    {200, ``},
		"status 500":    {500, `<root><currentgame>0</currentgame></root>`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := serve(t, tc.status, tc.body)
			if got, err := c.CurrentGame(context.Background()); err == nil {
				t.Fatalf("want error, got %d", got)
			}
		})
	}
}

func TestCurrentGame_Path(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`<root><currentgame>0</currentgame></root>`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := c.CurrentGame(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/serverinfo" {
		t.Fatalf("got %s %s", method, path)
	}
}

func TestCurrentGame_Negative(t *testing.T) {
	c := serve(t, 200, `<root><currentgame>-2147483648</currentgame></root>`)
	got, err := c.CurrentGame(context.Background())
	if err != nil || got != -2147483648 {
		t.Fatalf("got %d, %v", got, err)
	}
}
