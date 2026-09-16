package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

// DialTerminal attaches to a live session. Closing the socket does not stop the process.
func (c *Client) DialTerminal(ctx context.Context, sessionID, clientID string) (*websocket.Conn, error) {
	base := strings.Replace(c.Base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	u, err := url.Parse(base + "/v0/sessions/" + url.PathEscape(sessionID) + "/terminal")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("token", c.Token)
	q.Set("client", clientID)
	u.RawQuery = q.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.Token}},
	})
	return conn, err
}
