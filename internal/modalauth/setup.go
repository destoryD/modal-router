package modalauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type SetupEndpointResult struct {
	BaseURL      string `json:"baseUrl"`
	APIKey       string `json:"apiKey"`
	EndpointName string `json:"endpointName"`
	EndpointID   string `json:"endpointId"`
	Error        string `json:"error,omitempty"`
}

func createEndpoint(baseURL, workspace, env, cookie, csrf, proxyURL, modelName string) SetupEndpointResult {
	result := SetupEndpointResult{}
	client := httpClient(proxyURL, 120*time.Second)

	if modelName == "" {
		modelName = "moonshotai/Kimi-K3"
	}

	// First create a proxy auth token (required for authenticated shared endpoints)
	tokenResult := createProxyAuthToken(baseURL, workspace, cookie, csrf, proxyURL)
	if tokenResult.Error != "" {
		result.Error = "failed to create proxy token: " + tokenResult.Error
		return result
	}
	result.APIKey = tokenResult.APIKey
	log.Printf("[setup] proxy auth token created: %s", maskKey(result.APIKey))

	createBody := map[string]interface{}{
		"name": "kimi-k3-endpoint",
		"model": map[string]interface{}{
			"baseModelRepoId": modelName,
		},
		"servingMode":       "ENDPOINT_SERVING_MODE_SHARED",
		"unauthenticated":   false,
		"proxyRegions":      []string{"us-west"},
		"computeRegion":     map[string]interface{}{"auto": map[string]interface{}{}},
		"apiSurfaces":       []string{"ENDPOINT_API_SURFACE_OPENAI_CHAT_COMPLETIONS"},
		"inputModalities":   []string{"ENDPOINT_INPUT_MODALITY_TEXT"},
		"environmentName":   env,
	}

	bodyBytes, _ := json.Marshal(createBody)
	createURL := fmt.Sprintf("%s/api/endpoints/%s/env/%s", baseURL, workspace, env)
	log.Printf("[setup] creating endpoint: POST %s", createURL)

	req, err := http.NewRequest("POST", createURL, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = "create endpoint request failed: " + err.Error()
		return result
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	if resp.StatusCode == 409 {
		log.Printf("[setup] endpoint already exists, fetching existing endpoint list")
		existing := findExistingEndpoint(client, baseURL, workspace, env, cookie, "kimi-k3-endpoint")
		if existing.EndpointID != "" {
			result.EndpointID = existing.EndpointID
			result.EndpointName = "kimi-k3-endpoint"
			result.BaseURL = existing.BaseURL
			log.Printf("[setup] found existing endpoint: id=%s baseUrl=%s", existing.EndpointID, existing.BaseURL)
			return result
		}
		result.Error = "endpoint already exists but could not find it in the list"
		return result
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("create endpoint failed: HTTP %d %s", resp.StatusCode, string(respBody))
		return result
	}

	var createResp map[string]interface{}
	json.Unmarshal(respBody, &createResp)

	endpointID := getString(createResp, "endpointId")
	if endpointID == "" {
		if id, ok := createResp["id"]; ok {
			endpointID = fmt.Sprintf("%v", id)
		}
	}
	result.EndpointID = endpointID
	result.EndpointName = "kimi-k3-endpoint"
	log.Printf("[setup] endpoint created: id=%s", endpointID)

	time.Sleep(5 * time.Second)

	if endpointID != "" {
		for attempt := 0; attempt < 6; attempt++ {
			detailURL := fmt.Sprintf("%s/api/endpoints/%s/id/%s", baseURL, workspace, endpointID)
			req2, _ := http.NewRequest("GET", detailURL, nil)
			req2.Header.Set("Cookie", cookie)
			req2.Header.Set("Accept", "application/json")
			resp2, err := client.Do(req2)
			if err != nil {
				time.Sleep(3 * time.Second)
				continue
			}
			detailBody, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
			resp2.Body.Close()
			if resp2.StatusCode >= 200 && resp2.StatusCode < 300 {
				var detail map[string]interface{}
				json.Unmarshal(detailBody, &detail)
				// endpointServiceUrl is an array of base URLs
				if urls, ok := detail["endpointServiceUrl"].([]interface{}); ok && len(urls) > 0 {
					if s, ok := urls[0].(string); ok {
						result.BaseURL = s
					}
				}
				if result.BaseURL == "" {
					result.BaseURL = getString(detail, "direct")
				}
				if result.BaseURL == "" {
					result.BaseURL = getString(detail, "baseUrl")
				}
				if result.BaseURL != "" {
					log.Printf("[setup] endpoint baseUrl: %s", result.BaseURL)
					break
				}
			}
			log.Printf("[setup] endpoint baseUrl not ready, retrying (%d/6)...", attempt+1)
			time.Sleep(5 * time.Second)
		}
	}

	// Shared endpoints expose an OpenAI-compatible base URL pattern
	if result.BaseURL == "" && result.EndpointName != "" {
		result.BaseURL = fmt.Sprintf("%s/v1/%s/%s/direct", baseURL, workspace, result.EndpointName)
		log.Printf("[setup] constructed base URL from pattern: %s", result.BaseURL)
	}

	return result
}

type TokenResult struct {
	APIKey string `json:"apiKey"`
	Error  string `json:"error,omitempty"`
}

func createProxyAuthToken(baseURL, workspace, cookie, csrf, proxyURL string) TokenResult {
	result := TokenResult{}
	client := httpClient(proxyURL, 30*time.Second)

	tokenBody := map[string]interface{}{
		"name": "auto-generated",
	}
	bodyBytes, _ := json.Marshal(tokenBody)
	tokenURL := fmt.Sprintf("%s/api/workspaces/%s/proxy-auth-tokens", baseURL, workspace)
	log.Printf("[setup] creating proxy auth token: POST %s", tokenURL)

	req, err := http.NewRequest("POST", tokenURL, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = "create token request failed: " + err.Error()
		return result
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("create token failed: HTTP %d %s", resp.StatusCode, string(respBody))
		return result
	}

	var tokenResp map[string]interface{}
	json.Unmarshal(respBody, &tokenResp)

	result.APIKey = getString(tokenResp, "tokenSecret")
	if result.APIKey == "" {
		result.APIKey = getString(tokenResp, "token")
	}
	if result.APIKey == "" {
		result.APIKey = getString(tokenResp, "key")
	}
	if result.APIKey == "" {
		result.APIKey = getString(tokenResp, "secret")
	}
	// Modal proxy auth token format: {tokenId}.{tokenSecret}
	tokenID := getString(tokenResp, "tokenId")
	if tokenID != "" && result.APIKey != "" && !strings.Contains(result.APIKey, ".") {
		result.APIKey = tokenID + "." + result.APIKey
	}
	log.Printf("[setup] proxy auth token created: %s", maskKey(result.APIKey))

	return result
}

type StripeResult struct {
	StripeURL string `json:"stripeUrl"`
	Error     string `json:"error,omitempty"`
}

func getStripeLink(baseURL, workspace, cookie, proxyURL string) StripeResult {
	result := StripeResult{}
	client := httpClientNoRedirect(proxyURL, 30*time.Second)

	stripeURL := fmt.Sprintf("%s/api/stripe/%s/add-payment-method", baseURL, workspace)
	log.Printf("[setup] fetching stripe link: GET %s", stripeURL)

	req, err := http.NewRequest("GET", stripeURL, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "text/html,application/json")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = "stripe request failed: " + err.Error()
		return result
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc != "" {
		if isStripeURL(loc) {
			result.StripeURL = loc
			log.Printf("[setup] stripe link (from redirect): %s", loc)
			return result
		}
		log.Printf("[setup] add-payment-method redirected to: %s (not stripe)", loc)
	}

	if resp.StatusCode == 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		bodyStr := string(body)
		stripeMatch := extractStripeURL(bodyStr)
		if stripeMatch != "" {
			result.StripeURL = stripeMatch
			log.Printf("[setup] stripe link (from body): %s", stripeMatch)
			return result
		}
	}

	log.Printf("[setup] add-payment-method returned HTTP %d, trying billing-portal", resp.StatusCode)
	portalURL := fmt.Sprintf("%s/api/stripe/%s/billing-portal", baseURL, workspace)
	req2, _ := http.NewRequest("GET", portalURL, nil)
	req2.Header.Set("Cookie", cookie)
	req2.Header.Set("Accept", "text/html,application/json")
	resp2, err := client.Do(req2)
	if err == nil {
		loc2 := resp2.Header.Get("Location")
		resp2.Body.Close()
		if loc2 != "" {
			if isStripeURL(loc2) {
				result.StripeURL = loc2
				log.Printf("[setup] stripe portal link: %s", loc2)
				return result
			}
			log.Printf("[setup] billing-portal redirected to: %s (not stripe)", loc2)
		}
	}

	result.Error = fmt.Sprintf("could not extract stripe link (HTTP %d)", resp.StatusCode)
	return result
}

func httpClientNoRedirect(proxyURL string, timeout time.Duration) *http.Client {
	c := httpClient(proxyURL, timeout)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

func extractStripeURL(raw string) string {
	patterns := []string{
		`https://checkout\.stripe\.com/c/[^\s"'<>\\]+`,
		`https://checkout\.stripe\.com/[a-z]/[^\s"'<>\\]+`,
		`https://buy\.stripe\.com/[^\s"'<>\\]+`,
		`https://billing\.stripe\.com/[^\s"'<>\\]+`,
	}
	for _, p := range patterns {
		m := regexFind(raw, p)
		if m != "" {
			return m
		}
	}
	return ""
}

func findExistingEndpoint(client *http.Client, baseURL, workspace, env, cookie, name string) SetupEndpointResult {
	result := SetupEndpointResult{}
	listURL := fmt.Sprintf("%s/api/endpoints/%s/env/%s", baseURL, workspace, env)
	req, _ := http.NewRequest("GET", listURL, nil)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		result.Error = "list endpoints failed: " + err.Error()
		return result
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	bodyStr := string(body)
	log.Printf("[setup] endpoint list response (len=%d)", len(bodyStr))

	var items []map[string]interface{}
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err == nil {
		switch v := raw.(type) {
		case []interface{}:
			for _, item := range v {
				if m, ok := item.(map[string]interface{}); ok {
					items = append(items, m)
				}
			}
		case map[string]interface{}:
			items = getArray(v, "items")
			if len(items) == 0 {
				items = getArray(v, "endpoints")
			}
		}
	}
	for _, item := range items {
		itemName := getString(item, "name")
		itemID := getString(item, "endpointId")
		if itemID == "" {
			if id, ok := item["id"]; ok {
				itemID = fmt.Sprintf("%v", id)
			}
		}
		log.Printf("[setup] endpoint item: id=%s name=%s baseUrl=%s", itemID, itemName, getString(item, "baseUrl"))
		result.EndpointID = itemID
		result.EndpointName = itemName
		if result.EndpointName == "" {
			result.EndpointName = name
		}
		result.BaseURL = getString(item, "baseUrl")
		if result.BaseURL == "" {
			result.BaseURL = getString(item, "url")
		}
		if result.BaseURL == "" {
			result.BaseURL = getString(item, "proxyUrl")
		}
		if result.BaseURL == "" {
			result.BaseURL = getString(item, "baseURL")
		}
		if result.BaseURL == "" && result.EndpointID != "" {
			// Fetch endpoint detail for base URL
			detailURL := fmt.Sprintf("%s/api/endpoints/%s/id/%s", baseURL, workspace, result.EndpointID)
			req3, _ := http.NewRequest("GET", detailURL, nil)
			req3.Header.Set("Cookie", cookie)
			req3.Header.Set("Accept", "application/json")
			resp3, err3 := client.Do(req3)
			if err3 == nil {
				detailBody, _ := io.ReadAll(io.LimitReader(resp3.Body, 1<<20))
				resp3.Body.Close()
				if resp3.StatusCode >= 200 && resp3.StatusCode < 300 {
					var detail map[string]interface{}
					json.Unmarshal(detailBody, &detail)
					// endpointServiceUrl is an array of base URLs
					serviceURLs := getArray(detail, "endpointServiceUrl")
					if len(serviceURLs) > 0 {
						result.BaseURL = getString(serviceURLs[0], "")
						if result.BaseURL == "" {
							if u, ok := serviceURLs[0][""]; ok {
								result.BaseURL = fmt.Sprintf("%v", u)
							}
						}
					}
					if result.BaseURL == "" {
						// Try as string array
						if urls, ok := detail["endpointServiceUrl"].([]interface{}); ok && len(urls) > 0 {
							if s, ok := urls[0].(string); ok {
								result.BaseURL = s
							}
						}
					}
					if result.BaseURL == "" {
						result.BaseURL = getString(detail, "direct")
					}
					if result.BaseURL == "" {
						result.BaseURL = getString(detail, "baseUrl")
					}
				}
			}
		}
		// Shared endpoints expose an OpenAI-compatible base URL:
		// https://modal.com/v1/{workspace}/{endpointName}/direct
		if result.BaseURL == "" && result.EndpointName != "" {
			result.BaseURL = fmt.Sprintf("%s/v1/%s/%s/direct", baseURL, workspace, result.EndpointName)
			log.Printf("[setup] constructed base URL from pattern: %s", result.BaseURL)
		}
		return result
	}
	result.Error = "no matching endpoint found in list"
	return result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func maskKey(key string) string {
	if len(key) <= 10 {
		return "***"
	}
	return key[:7] + "..." + key[len(key)-4:]
}

func isStripeURL(url string) bool {
	lower := strings.ToLower(url)
	return strings.Contains(lower, "stripe.com") ||
		strings.Contains(lower, "billing.modal.com/c/pay") ||
		strings.Contains(lower, "/c/pay/cs_") ||
		strings.Contains(lower, "checkout.stripe.com")
}
