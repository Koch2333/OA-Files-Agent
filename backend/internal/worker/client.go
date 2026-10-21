// Package worker checks the local document process without exposing credentials.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func New(baseURL, token string) (*Client, error) {
	c := &Client{}
	if baseURL == "" || token == "" {
		return c, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("invalid worker URL")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("worker URL must be a loopback HTTP origin")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("worker token must contain at least 32 printable ASCII characters")
	}
	for _, ch := range token {
		if ch < 33 || ch > 126 {
			return nil, errors.New("worker token must contain at least 32 printable ASCII characters")
		}
	}
	c.endpoint = strings.TrimSuffix(baseURL, "/") + "/healthz"
	c.token = token
	// Disable environment proxies and redirects so the bearer token stays on loopback.
	c.http = &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func (c *Client) Check(ctx context.Context) string {
	if c.endpoint == "" {
		return "not_configured"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return "unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "timeout"
		}
		var timed interface{ Timeout() bool }
		if errors.As(err, &timed) && timed.Timeout() {
			return "timeout"
		}
		return "unavailable"
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return "unauthorized"
	}
	if res.StatusCode != http.StatusOK {
		return "unavailable"
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(payload) > 4096 {
		return "invalid_response"
	}
	var health struct {
		Data struct {
			Status              string `json:"status"`
			ExtractionAvailable *bool  `json:"extraction_available"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &health) != nil || health.Data.Status != "ok" || health.Data.ExtractionAvailable == nil {
		return "invalid_response"
	}
	return "ok"
}
