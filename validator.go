package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Job struct {
	Status       string
	Total        int
	Success      int
	Failed       int
	CurrentDomain string
	CurrentCount int
	DownloadURL  string
	mu           sync.Mutex
}

var (
	jobs = make(map[string]*Job)
	jobsMu sync.Mutex
	workDir = "./validator_work"
)

func init() {
	os.MkdirAll(workDir, 0755)
}

func check(e error) {
    if e != nil {
        panic(e)
    }
}

func slugify(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)[:16]
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok"}`)
}

func authHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"auth is not configured"}`)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(apiKey)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
		return
	}
	fmt.Fprint(w, `{"status":"authenticated"}`)
}

func loopbackHandler(w http.ResponseWriter, r *http.Request) {
//     w.Header().Set("Content-Type", "plain/text")
//     fmt.Fprintf(w, "Method: %s, Content-Type: %s\n", r.Method, r.Header.Get("Content-Type"))

    if r.Body == nil {
          fmt.Fprintln(w, "Body is nil")
          http.Error(w, "body is nil", 400)
          return
    }

    body, _ := io.ReadAll(r.Body)
//     fmt.Fprintf(w, "Body length: %d, content: %s\n", len(body), string(body))
    w.Write(body)
}

func outputHandler(w http.ResponseWriter, r *http.Request) {
    linkStr := r.URL.Query().Get("link")
    if linkStr == "" {
            http.Error(w, "missing ?link parameter", 400)
            return
    }

    slug := slugify(linkStr)
	resultFile := filepath.Join(workDir, slug+".txt")
	_, err := os.Stat(resultFile)
// 	check(err)
	if (errors.Is(err, os.ErrNotExist)) {
        fmt.Fprintf(w, "No file found")
	    return
	}
	data, err := os.ReadFile(resultFile)
	check(err)
	w.Write(data)
}

func validateHandler(w http.ResponseWriter, r *http.Request) {
	var domains []string
	var source string
	offset := 0
	uuid := r.URL.Query().Get("uuid")
	if o := r.URL.Query().Get("offset"); o != "" {
		offset, _ = strconv.Atoi(o)
	}

	// Try body first (local testing)
	if r.Method == "POST" && r.Body != nil {
		scanner := bufio.NewScanner(r.Body)
		for scanner.Scan() {
			domain := strings.TrimSpace(scanner.Text())
			if domain == "" {
			    continue
			}
			if strings.HasPrefix(domain, "{") || strings.HasPrefix(domain, "[") { // json data
				continue
			}

            domains = append(domains, domain)
		}
		source = "body"
	}

	// Fallback to ?link parameter
	linkStr := ""
	if len(domains) == 0 {
		linkStr = r.URL.Query().Get("link")
		if linkStr == "" {
			http.Error(w, "missing body domains or ?link parameter", 400)
			return
		}
		source = "link:" + linkStr
	}

    if (uuid != "") {
        source = uuid // better than slug
    }
//     w.Header().Set("Content-Type", "application/json")
//     fmt.Fprintf(w, `{"method":"%s","body":"%s","domains":"%s"}`,
//         r.Method, r.Body, strings.Join(domains, " _ "))
//     return

	slug := slugify(source)
	jobsMu.Lock()
	job, exists := jobs[slug]
	jobsMu.Unlock()

    exists = false
	if !exists {
		job = &Job{Status: "pending"}
		jobsMu.Lock()
		jobs[slug] = job
		jobsMu.Unlock()

		processJob(slug, domains, linkStr, offset)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"slug":"%s","status":"%s","total":%d,"success":%d,"failed":%d,"current_domain":"%s","current_count":%d,"download_url":"%s"}`,
		source, job.Status, job.Total, job.Success, job.Failed, job.CurrentDomain, job.CurrentCount, job.DownloadURL)
}

func processJob(slug string, domains []string, linkStr string, offset int) {
	if linkStr != "" {
		resp, err := http.Get(linkStr)
		if err != nil {
			markJobFailed(slug)
			return
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		domains = make([]string, 0)
		for scanner.Scan() {
			domain := strings.TrimSpace(scanner.Text())
			if domain != "" {
				domains = append(domains, domain)
			}
		}
		if err := scanner.Err(); err != nil {
			markJobFailed(slug)
			return
		}
	}

	processDomains(slug, domains, offset)
}

func markJobFailed(slug string) {
	jobsMu.Lock()
	job := jobs[slug]
	jobsMu.Unlock()
	if job == nil {
		return
	}

	job.mu.Lock()
	job.Status = "failed"
	job.mu.Unlock()
}

func processDomains(slug string, domains []string, offset int) {
	job := jobs[slug]
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()

	resultFile := filepath.Join(workDir, slug+".txt")
	progressFile := filepath.Join(workDir, slug+".progress")
	outFile, _ := os.Create(resultFile)
	defer outFile.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	job.mu.Lock()
	job.Total = len(domains)
	job.mu.Unlock()

	sem := make(chan struct{}, 2000)
	var wg sync.WaitGroup

	for i, domain := range domains {
        fmt.Printf("%d _ " + domain + "\r\n", i)
		if i < offset {
			continue
		}

		wg.Add(1)
		go func(d string, idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			finalURL := checkDomain(d, client)
			if finalURL != "" {
				outFile.WriteString(finalURL + "\n")
				job.mu.Lock()
				job.Success++
				job.CurrentCount = job.Success + job.Failed
				job.CurrentDomain = d
				job.mu.Unlock()
			} else {
				job.mu.Lock()
				job.Failed++
				job.CurrentCount = job.Success + job.Failed
				job.CurrentDomain = d
				job.mu.Unlock()
			}

			if idx%100 == 0 {
				os.WriteFile(progressFile, []byte(strconv.Itoa(idx)), 0644)
			}
		}(domain, i)
	}

	wg.Wait()

	outFile.Sync()
	job.mu.Lock()
	job.Status = "completed"
	job.DownloadURL = fmt.Sprintf("file://%s", resultFile)
	job.mu.Unlock()

	os.Remove(progressFile)
}

func checkDomain(domain string, client *http.Client) string {
    if (!strings.HasPrefix(domain, "http")) {
		domain = "https://" + domain
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", domain, nil)
	check(err)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ""
	}

	// Follow redirects if 3xx
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		location := resp.Header.Get("Location")
		if location != "" {
			if !strings.HasPrefix(location, "http") {
				base, _ := url.Parse(domain)
				location = base.Scheme + "://" + base.Host + location
			}
			return checkDomain(location, client)
		}
	}

	// Return full final URL
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.Request.URL.String()
	}

	return ""
}
