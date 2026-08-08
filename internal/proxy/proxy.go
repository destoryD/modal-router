package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"modals-router/internal/balancer"
	"modals-router/internal/models"
	"modals-router/internal/store"
)

var hopByHop = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

type Proxy struct {
	store      *store.Store
	balancer   *balancer.Balancer
	client     *http.Client
	maxRetries int
}

func New(s *store.Store, b *balancer.Balancer, maxRetries int) *Proxy {
	if maxRetries < 1 {
		maxRetries = 1
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   0,
	}
	return &Proxy{
		store:      s,
		balancer:   b,
		client:     client,
		maxRetries: maxRetries,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var lastErr error
	var lastStatus int

	for attempt := 0; attempt < p.maxRetries; attempt++ {
		ch := p.balancer.Select()
		if ch == nil {
			break
		}

		p.store.IncrRequestCount(ch.ID)

		upstreamURL := strings.TrimRight(ch.URL, "/") + r.URL.Path
		if r.URL.RawQuery != "" {
			upstreamURL += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			p.store.IncrFailCount(ch.ID)
			continue
		}

		copyRequestHeaders(req.Header, r.Header)
		authHeader := ch.AuthHeader
		if authHeader == "" {
			authHeader = "Authorization"
		}
		authPrefix := ch.AuthPrefix
		if authPrefix == "" {
			authPrefix = "Bearer "
		}
		req.Header.Set(authHeader, authPrefix+ch.Key)
		for k, v := range ch.Headers {
			req.Header.Set(k, v)
		}

		start := time.Now()
		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			p.store.IncrFailCount(ch.ID)
			log.Printf("proxy error: channel=%s url=%s err=%v", ch.Name, upstreamURL, err)
			continue
		}

		if shouldDisable(ch.DisableOnStatus, resp.StatusCode) {
			reason := fmt.Sprintf("HTTP %d", resp.StatusCode)
			p.store.DisableChannel(ch.ID, reason)
			p.balancer.Reload(p.store.ListChannels())
			log.Printf("channel %s (%s) disabled: %s", ch.ID, ch.Name, reason)
			resp.Body.Close()
			lastErr = fmt.Errorf("channel %s returned %d", ch.Name, resp.StatusCode)
			lastStatus = resp.StatusCode
			continue
		}

		p.store.IncrSuccessCount(ch.ID)
		log.Printf("proxy: %s %s -> %s channel=%s status=%d %s",
			r.Method, r.URL.Path, ch.Name, ch.Name, resp.StatusCode, time.Since(start))

		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		streamBody(w, resp.Body)
		resp.Body.Close()
		return
	}

	if lastStatus != 0 {
		http.Error(w, fmt.Sprintf("all channels failed: %v", lastErr), http.StatusBadGateway)
	} else if lastErr != nil {
		http.Error(w, fmt.Sprintf("all channels failed: %v", lastErr), http.StatusBadGateway)
	} else {
		http.Error(w, `{"error":{"message":"no channels available","type":"router_error"}}`,
			http.StatusServiceUnavailable)
	}
}

func shouldDisable(codes []int, status int) bool {
	for _, c := range codes {
		if c == status {
			return true
		}
	}
	return false
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] {
			continue
		}
		if ck == "Authorization" || ck == "Accept-Encoding" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] {
			continue
		}
		if ck == "Content-Length" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func streamBody(w http.ResponseWriter, body io.ReadCloser) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

type TestResult struct {
	Success      bool   `json:"success"`
	StatusCode   int    `json:"status_code"`
	LatencyMS    int64  `json:"latency_ms"`
	Response     string `json:"response"`
	WouldDisable bool   `json:"would_disable"`
	Error        string `json:"error,omitempty"`
}

func (p *Proxy) TestChannel(ch models.Channel, model string) TestResult {
	if model == "" {
		model = "kimi-k3"
	}

	testBody, _ := json.Marshal(map[string]interface{}{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Say hello in one word."}},
		"max_tokens": 10,
		"stream":     false,
	})

	upstreamURL := strings.TrimRight(ch.URL, "/") + "/v1/chat/completions"

	req, err := http.NewRequest("POST", upstreamURL, bytes.NewReader(testBody))
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	authHeader := ch.AuthHeader
	if authHeader == "" {
		authHeader = "Authorization"
	}
	authPrefix := ch.AuthPrefix
	if authPrefix == "" {
		authPrefix = "Bearer "
	}
	req.Header.Set(authHeader, authPrefix+ch.Key)
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}

	testClient := &http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	resp, err := testClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{Error: err.Error(), LatencyMS: latency}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	respStr := string(respBody)

	return TestResult{
		Success:      resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode:   resp.StatusCode,
		LatencyMS:    latency,
		Response:     respStr,
		WouldDisable: shouldDisable(ch.DisableOnStatus, resp.StatusCode),
	}
}
