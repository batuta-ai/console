package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func New(base, token string) *Client {
	return &Client{Base: base, Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, b)
	}
	if out == nil || res.StatusCode == 204 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (c *Client) Get(path string, out any) error { return c.Do(http.MethodGet, path, nil, out) }
func (c *Client) Post(path string, body, out any) error {
	return c.Do(http.MethodPost, path, body, out)
}

func (c *Client) Alive() bool {
	var v map[string]any
	return c.Get("/v0/capabilities", &v) == nil
}
