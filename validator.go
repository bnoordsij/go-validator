package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
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
	workDir = "/tmp/validator_work"
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

func validateHandler(w http.ResponseWriter, r *http.Request) {
	linkStr := r.URL.Query().Get("link")
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		offset, _ = strconv.Atoi(o)
	}

	if linkStr == "" {
		http.Error(w, "missing ?link parameter", 400)
		return
	}

	slug := slugify(linkStr)
	jobsMu.Lock()
	job, exists := jobs[slug]
	jobsMu.Unlock()

	if !exists {
		job = &Job{Status: "pending"}
		jobsMu.Lock()
		jobs[slug] = job
		jobsMu.Unlock()

		go processJob(slug, linkStr, offset)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"%s","total":%d,"success":%d,"failed":%d,"current_domain":"%s","current_count":%d,"download_url":"%s"}`,
		job.Status, job.Total, job.Success, job.Failed, job.CurrentDomain, job.CurrentCount, job.DownloadURL)
}

func outputHandler(w http.ResponseWriter, r *http.Request) {
        linkStr := r.URL.Query().Get("link")
        if linkStr == "" {
                http.Error(w, "missing ?link parameter", 400)
                return
        }

        slug := slugify(linkStr)
	resultFile := filepath.Join(workDir, slug+".txt")
	data, err := os.ReadFile(resultFile)
	check(err)
    	fmt.Fprintf(w, string(data))
}

func processJob(slug, linkStr string, offset int) {
	job := jobs[slug]
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()

	// Download domain list
	resp, err := http.Get(linkStr)
	if err != nil {
		job.mu.Lock()
		job.Status = "failed"
		job.mu.Unlock()
		return
	}
	defer resp.Body.Close()

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

	scanner := bufio.NewScanner(resp.Body)
	domains := []string{}
	count := 0
	for scanner.Scan() {
		domains = append(domains, strings.TrimSpace(scanner.Text()))
		count++
	}

	job.mu.Lock()
	job.Total = len(domains)
	job.mu.Unlock()

	// TODO: implement proper batching for very large lists
	sem := make(chan struct{}, 2000)
	var wg sync.WaitGroup

	for i, domain := range domains {
		if i < offset {
			continue
		}

		wg.Add(1)
		go func(d string, idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			finalDomain := checkDomain(d, client)
			if finalDomain != "" {
				outFile.WriteString(finalDomain + "\n")
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

			if idx%1000 == 0 {
				os.WriteFile(progressFile, []byte(strconv.Itoa(idx)), 0644)
			}
		}(domain, i)
	}

	wg.Wait()

	outFile.Sync()
	// TODO: upload results to storage service
	job.mu.Lock()
	job.Status = "completed"
	job.DownloadURL = fmt.Sprintf("file://%s", resultFile)
	job.mu.Unlock()

	os.Remove(progressFile)
}

func checkDomain(domain string, client *http.Client) string {
	if !strings.HasPrefix(domain, "http") {
		domain = "https://" + domain
	}

	req, _ := http.NewRequest("HEAD", domain, nil)
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

	// Extract domain from final URL
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		u, _ := url.Parse(resp.Request.URL.String())
		return u.Host
	}

	return ""
}

