package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAuthEndpointVerifiesAPIKey(t *testing.T) {
	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		t.Skip("API_KEY is not set in the environment")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth", authHandler)
	server := httptest.NewServer(mux)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/auth", nil)
	if err != nil {
		t.Fatalf("create auth request: %v", err)
	}
	req.Header.Set("X-API-Key", apiKey)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /auth status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode auth response: %v", err)
	}
	if result.Status != "authenticated" {
		t.Fatalf("auth response status = %q, want authenticated", result.Status)
	}

	badReq, err := http.NewRequest(http.MethodPost, server.URL+"/auth", nil)
	if err != nil {
		t.Fatalf("create invalid auth request: %v", err)
	}
	badReq.Header.Set("X-API-Key", apiKey+"-invalid")
	badResp, err := server.Client().Do(badReq)
	if err != nil {
		t.Fatalf("POST /auth with invalid key: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /auth with invalid key status = %d, want %d", badResp.StatusCode, http.StatusUnauthorized)
	}
}

func TestValidatePostBodyWritesDomainsAndReturnsJSON(t *testing.T) {
	oldWorkDir := workDir
	workDir = t.TempDir()
	defer func() { workDir = oldWorkDir }()

	jobsMu.Lock()
	oldJobs := jobs
	jobs = make(map[string]*Job)
	jobsMu.Unlock()
	defer func() {
		jobsMu.Lock()
		jobs = oldJobs
		jobsMu.Unlock()
	}()

	fakeDomains := []string{"example.com", "example.org", "example.net"}
	fakeHosts := make(map[string]struct{}, len(fakeDomains))
	for _, domain := range fakeDomains {
		fakeHosts[domain] = struct{}{}
	}

	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := fakeHosts[req.URL.Hostname()]; ok {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		return oldTransport.RoundTrip(req)
	})
	defer func() { http.DefaultTransport = oldTransport }()

	server := httptest.NewServer(http.HandlerFunc(validateHandler))
	defer server.Close()

	resp, err := http.Post(server.URL+"/validate", "text/plain", strings.NewReader(strings.Join(fakeDomains, "\n")))
	if err != nil {
		t.Fatalf("POST /validate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /validate status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}

	var result struct {
		Slug    string `json:"slug"`
		Status  string `json:"status"`
		Total   int    `json:"total"`
		Success int    `json:"success"`
		Failed  int    `json:"failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("response status = %q, want completed", result.Status)
	}
	if result.Total != len(fakeDomains) {
		t.Fatalf("response total = %d, want %d", result.Total, len(fakeDomains))
	}
	if result.Success != len(fakeDomains) || result.Failed != 0 {
		t.Fatalf("response success/failed = %d/%d, want %d/0", result.Success, result.Failed, len(fakeDomains))
	}

	resultFile := filepath.Join(workDir, slugify("body")+".txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(resultFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == len(fakeDomains) {
				for _, domain := range fakeDomains {
					if !strings.Contains(string(data), domain) {
						t.Fatalf("results file does not contain %q: %q", domain, data)
					}
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	data, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatalf("read results file: %v", err)
	}
	lineCount := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	t.Fatalf("results file %q has %d lines, want %d: %q", resultFile, lineCount, len(fakeDomains), data)
}
