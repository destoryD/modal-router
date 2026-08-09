package modalauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func workspaceFromURL(workspaceURL string) string {
	value := strings.TrimSpace(workspaceURL)
	if value == "" {
		return ""
	}
	if idx := strings.Index(value, "://"); idx >= 0 {
		value = value[idx+3:]
	}
	value = strings.Trim(value, "/")
	parts := strings.Split(value, "/")
	for i, p := range parts {
		if p == "apps" || p == "endpoints" || p == "settings" {
			if i+1 < len(parts) {
				ws := strings.TrimSpace(parts[i+1])
				if ws != "" {
					return ws
				}
			}
		}
	}
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p != "" && p != "main" && p != "onboarding" && !strings.Contains(p, "?") {
			return p
		}
	}
	return ""
}

func fetchBalance(baseURL, workspace, cookie, proxyURL string) (balance, creditsUsed, creditsLimit, plan string, err error) {
	if workspace == "" {
		return "", "", "", "", fmt.Errorf("workspace is empty")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://modal.com"
	}

	client := httpClient(proxyURL, 15*time.Second)

	// Fetch workspace data (has cycleUsage, grantedCycleCredits, planType)
	wsData, wsErr := fetchJSON(client, baseURL+"/api/workspaces/"+workspace, cookie)
	if wsErr == nil {
		plan = getString(wsData, "planType")
		cycleUsage := getFloat(wsData, "cycleUsage")
		granted := getFloat(wsData, "grantedCycleCredits")
		if cycleUsage > 0 {
			creditsUsed = fmt.Sprintf("%.2f", cycleUsage)
		}
		if granted > 0 {
			creditsLimit = fmt.Sprintf("%.2f", granted)
		}
		remaining := granted - cycleUsage
		if remaining > 0 {
			balance = fmt.Sprintf("$%.2f", remaining)
		}
	}

	// Fetch credit-summary (has currentCyclePlanCredits)
	csData, csErr := fetchJSON(client, baseURL+"/api/workspaces/"+workspace+"/credit-summary", cookie)
	if csErr == nil {
		planCredits := getFloat(csData, "currentCyclePlanCredits")
		if planCredits > 0 && creditsLimit == "" {
			creditsLimit = fmt.Sprintf("%.2f", planCredits)
		}
		if balance == "" && planCredits > 0 {
			used := parseFloatSafe(creditsUsed)
			remaining := planCredits - used
			balance = fmt.Sprintf("$%.2f", remaining)
		}
	}

	// Fetch credits (has grants with additionalGrants, totalThresholdCredits)
	credData, credErr := fetchJSON(client, baseURL+"/api/workspaces/"+workspace+"/credits", cookie)
	if credErr == nil {
		grants := getArray(credData, "grants")
		if len(grants) > 0 {
			totalThreshold := getFloat(grants[0], "totalThresholdCredits")
			if totalThreshold > 0 && creditsLimit == "" {
				creditsLimit = fmt.Sprintf("%.2f", totalThreshold)
			}
		}
	}

	if balance == "" && creditsLimit != "" {
		balance = "$0.00"
	}

	return balance, creditsUsed, creditsLimit, plan, nil
}

func fetchJSON(client *http.Client, reqURL, cookie string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func httpClient(proxyURL string, timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func getString(obj map[string]interface{}, key string) string {
	if v, ok := obj[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getFloat(obj map[string]interface{}, key string) float64 {
	if v, ok := obj[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case string:
			return parseFloatSafe(n)
		}
	}
	return 0
}

func getArray(obj map[string]interface{}, key string) []map[string]interface{} {
	if v, ok := obj[key]; ok {
		if arr, ok := v.([]interface{}); ok {
			out := make([]map[string]interface{}, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}

func parseFloatSafe(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	return f
}

func regexFind(raw, pattern string) string {
	re := regexp.MustCompile(pattern)
	match := re.FindString(raw)
	return match
}

func (s *Store) SyncBalance(id string) error {
	s.mu.Lock()
	idx := s.findAccountLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("account not found")
	}
	acc := s.state.Accounts[idx]
	proxyURL := s.state.Settings.ProxyURL
	baseURL := s.state.Settings.BaseURL
	if acc.CookieCipher == "" {
		s.mu.Unlock()
		return fmt.Errorf("cookie missing")
	}
	cookie, err := s.decrypt(acc.CookieCipher)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	workspace := acc.Workspace
	if workspace == "" {
		workspace = workspaceFromURL(acc.WorkspaceURL)
	}
	if workspace == "" {
		return fmt.Errorf("workspace unknown; run setup first")
	}

	balance, creditsUsed, creditsLimit, plan, err := fetchBalance(baseURL, workspace, cookie, proxyURL)

	s.mu.Lock()
	defer s.mu.Unlock()
	idx = s.findAccountLocked(id)
	if idx < 0 {
		return fmt.Errorf("account not found")
	}
	s.state.Accounts[idx].Workspace = workspace
	if err == nil {
		s.state.Accounts[idx].Balance = balance
		s.state.Accounts[idx].CreditsUsed = creditsUsed
		s.state.Accounts[idx].CreditsLimit = creditsLimit
		s.state.Accounts[idx].Plan = plan
		s.state.Accounts[idx].UpdatedAt = time.Now()
	}
	return s.saveLocked()
}
