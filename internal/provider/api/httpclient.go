package api

import (
	"net"
	"net/http"
	"time"
)

// Connection limits for a streaming client. They bound reaching the
// server, never the response: a model can take minutes to read a large
// prompt before its first byte, and stream for many more after it. The
// harness ends a request that has gone quiet instead
// (internal/harness/stall.go).
const (
	dialTimeout         = 30 * time.Second
	tlsHandshakeTimeout = 30 * time.Second
)

// NewStreamingClient returns an HTTP client for model requests: proxy
// settings from the environment, bounded connect and TLS handshake, and no
// total Timeout. http.Client.Timeout covers reading the whole body, so any
// value there cuts off a slow but progressing stream.
func NewStreamingClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}}
}
