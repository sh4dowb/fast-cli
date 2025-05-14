package main

import (
	"bytes"
	crand "crypto/rand" // aliased to avoid conflict with math/rand if used
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"context"
)

const (
	// API Configuration
	fastComBaseURL   = "https://api.fast.com/netflix/speedtest/v2"
	fastComToken     = "YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm" // Provided token
	defaultURLCount  = 5                                  // Number of server URLs to initially fetch
	numServersToTest = 3                                  // Number of top servers to use for actual tests

	// Test Configuration
	downloadTestDuration   = 15 * time.Second     // Duration for the download test
	downloadChunkSizeBytes = 25 * 1024 * 1024     // 25 MiB chunk size for download
	uploadTestDuration     = 15 * time.Second     // Duration for the upload test
	uploadChunkSizeBytes   = 10 * 1024 * 1024     // 10 MiB chunk size for upload

	// Network
	httpClientTimeout = 60 * time.Second
	userAgent         = "go-speedtest-cli/0.1"
)

// API Response Structures
type apiResponse struct {
	Client  clientInfo `json:"client"`
	Targets []target   `json:"targets"`
}

type clientInfo struct {
	IP       string   `json:"ip"`
	Asn      string   `json:"asn"`
	Location location `json:"location"`
}

type location struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

type target struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Location location `json:"location"`
}

// Ping Result Structure
type pingedTarget struct {
	Target  target
	Latency time.Duration
	Err     error
}

// HTTP Client
var httpClient = &http.Client{
	Timeout: httpClientTimeout,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// modifySpeedtestURL helper to change /speedtest to /speedtest/newSegment
// e.g., /speedtest?query -> /speedtest/range/0-0?query
// or /speedtest -> /speedtest/range/0-0
func modifySpeedtestURL(originalURL string, pathSegmentToAdd string) string {
	const speedtestPath = "/speedtest"
	if strings.Contains(originalURL, speedtestPath+"?") {
		return strings.Replace(originalURL, speedtestPath+"?", speedtestPath+pathSegmentToAdd+"?", 1)
	} else if strings.HasSuffix(originalURL, speedtestPath) {
		// To handle cases like example.com/speedtest (no query)
		return strings.Replace(originalURL, speedtestPath, speedtestPath+pathSegmentToAdd, 1)
	}
	log.Printf("Warning: URL pattern for modification not fully matched: %s", originalURL)
	// Attempt a general replacement if specific patterns fail, might be less robust
	return strings.Replace(originalURL, speedtestPath, speedtestPath+pathSegmentToAdd, 1)

}

func fetchTestServers() ([]target, error) {
	apiURL := fmt.Sprintf("%s?https=true&token=%s&urlCount=%d", fastComBaseURL, fastComToken, defaultURLCount)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching server list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server list API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decoding server list JSON: %w", err)
	}

	return apiResp.Targets, nil
}

func measurePings(targetsToPing []target) []pingedTarget {
	var wg sync.WaitGroup
	resultsChan := make(chan pingedTarget, len(targetsToPing))

	for _, t := range targetsToPing {
		wg.Add(1)
		go func(srv target) {
			defer wg.Done()
			pingURL := modifySpeedtestURL(srv.URL, "/range/0-0")

			req, err := http.NewRequest("GET", pingURL, nil)
			if err != nil {
				resultsChan <- pingedTarget{Target: srv, Err: fmt.Errorf("creating ping request: %w", err)}
				return
			}
			req.Header.Set("User-Agent", userAgent)

			start := time.Now()
			resp, err := httpClient.Do(req)
			latency := time.Since(start)

			if err != nil {
				resultsChan <- pingedTarget{Target: srv, Latency: latency, Err: err}
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body) // Ensure body is read and closed

			if resp.StatusCode != http.StatusOK {
				resultsChan <- pingedTarget{Target: srv, Latency: latency, Err: fmt.Errorf("ping failed with status %d", resp.StatusCode)}
				return
			}
			resultsChan <- pingedTarget{Target: srv, Latency: latency}
		}(t)
	}

	wg.Wait()
	close(resultsChan)

	var pingedTargetsResult []pingedTarget
	for res := range resultsChan {
		pingedTargetsResult = append(pingedTargetsResult, res)
	}

	// Filter out errors and sort by latency
	var successfulPings []pingedTarget
	for _, pt := range pingedTargetsResult {
		if pt.Err == nil {
			successfulPings = append(successfulPings, pt)
		} else {
			log.Printf("Ping error for %s: %v\n", pt.Target.Name, pt.Err)
		}
	}

	sort.Slice(successfulPings, func(i, j int) bool {
		return successfulPings[i].Latency < successfulPings[j].Latency
	})

	return successfulPings
}

func performDownloadTest(servers []target, testDuration time.Duration, chunkSize int) (float64, error) {
	if len(servers) == 0 {
		return 0, fmt.Errorf("no servers available for download test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	var wg sync.WaitGroup
	var totalBytesDownloaded int64
	var totalBytesDownloadedMutex sync.Mutex // Mutex still fine for sum, or use atomic.AddInt64
	errorsChan := make(chan error, len(servers)*5) // Increased buffer in case of multiple errors per goroutine

	fmt.Printf("Starting download from %d server(s) for %s, chunk size %d bytes...\n", len(servers), testDuration, chunkSize)

	for _, srv := range servers {
		wg.Add(1)
		go func(s target) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done(): // Test duration elapsed or explicitly cancelled
					return
				default:
					// Continue downloading next chunk
				}

				downloadURL := modifySpeedtestURL(s.URL, fmt.Sprintf("/range/0-%d", chunkSize-1)) // range is 0-indexed

				req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
				if err != nil {
					// If context is done, this is not an unexpected error for this request
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: creating download request: %w", s.Name, err)
					}
					return // Stop this goroutine
				}
				req.Header.Set("User-Agent", userAgent)

				resp, err := httpClient.Do(req)
				if err != nil {
					if ctx.Err() == nil { // Don't report error if it's due to context cancellation
						errorsChan <- fmt.Errorf("server %s: download request error: %w", s.Name, err)
					}
					return // Stop this goroutine on significant error
				}

				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
					bodyBytes, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: download failed with status %d: %s", s.Name, resp.StatusCode, string(bodyBytes))
					}
					return // Stop this goroutine
				}

				written, err := io.Copy(io.Discard, resp.Body)
				resp.Body.Close() // Ensure body is closed

				if err != nil {
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: error reading download body: %w", s.Name, err)
					}
					return // Stop this goroutine
				}

				totalBytesDownloadedMutex.Lock()
				totalBytesDownloaded += written
				totalBytesDownloadedMutex.Unlock()

				// If we received less than requested, and context is not done,
				// it might be end of stream or server limit for that specific request.
				// The loop will continue trying to fetch more unless context is done.
				if written < int64(chunkSize) && ctx.Err() == nil {
					// Optional: log this, but loop continues
					// log.Printf("Server %s sent %d bytes, expected up to %d for this chunk", s.Name, written, chunkSize)
				}
			}
		}(srv)
	}

	wg.Wait()
	close(errorsChan)

	for err := range errorsChan {
		log.Printf("Download stream error: %v\n", err)
	}

	// Use the actual testDuration for calculation, as it's the controlled variable.
	// totalBytesDownloaded will be the sum from all successful chunk downloads.
	if testDuration.Seconds() == 0 || totalBytesDownloaded == 0 {
		// Check if ctx.Err() indicates premature stop for a different reason if needed.
		// For now, if no bytes or no time (which shouldn't happen for testDuration), return 0.
		return 0, fmt.Errorf("download test yielded no data or test duration was zero")
	}

	// Speed in Mbps (Megabits per second)
	speedMbps := (float64(totalBytesDownloaded) * 8) / (testDuration.Seconds() * 1000000)
	return speedMbps, nil
}

func performUploadTest(servers []target, testDuration time.Duration, chunkSize int) (float64, error) {
	if len(servers) == 0 {
		return 0, fmt.Errorf("no servers available for upload test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	var wg sync.WaitGroup
	var totalBytesUploaded int64
	var totalBytesUploadedMutex sync.Mutex // Mutex still fine, or use atomic.AddInt64
	errorsChan := make(chan error, len(servers)*5)

	fmt.Printf("Starting upload to %d server(s) for %s, chunk size %d bytes...\n", len(servers), testDuration, chunkSize)

	randomDataBase := make([]byte, chunkSize) // Pre-allocate base for random data
	_, err := crand.Read(randomDataBase)
	if err != nil {
		return 0, fmt.Errorf("failed to generate initial random data for upload: %w", err)
	}

	for _, srv := range servers {
		wg.Add(1)
		go func(s target) {
			defer wg.Done()

			// Each goroutine can reuse a slice for its random data, but needs to fill it.
			// Or, if crypto/rand is fast enough, generate each time.
			// For simplicity here, let's assume we'll generate fresh random data or copy from a base.
			// Small optimization: copy from pre-generated base to avoid repeated crand.Read calls in tight loop
			// if performance of crand.Read becomes an issue. Here, new generation per chunk is fine.

			for {
				select {
				case <-ctx.Done(): // Test duration elapsed or explicitly cancelled
					return
				default:
					// Continue uploading next chunk
				}

				// It's important to generate new random data for each POST to avoid network/server-side caching/compression
				// tricks that might inflate speed results.
				currentChunkData := make([]byte, chunkSize)
				n, err := crand.Read(currentChunkData)
				if err != nil || n != chunkSize {
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: generating random data: %w", s.Name, err)
					}
					return // Stop this goroutine
				}
				body := bytes.NewReader(currentChunkData)

				req, err := http.NewRequestWithContext(ctx, "POST", s.URL, body)
				if err != nil {
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: creating upload request: %w", s.Name, err)
					}
					return // Stop this goroutine
				}
				req.Header.Set("User-Agent", userAgent)
				req.Header.Set("Content-Type", "application/octet-stream")
				req.ContentLength = int64(chunkSize)

				resp, err := httpClient.Do(req)
				if err != nil {
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: upload request error: %w", s.Name, err)
					}
					return // Stop this goroutine
				}

				// Consume and close response body
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
					if ctx.Err() == nil {
						errorsChan <- fmt.Errorf("server %s: upload failed with status %d", s.Name, resp.StatusCode)
					}
					return // Stop this goroutine
				}

				totalBytesUploadedMutex.Lock()
				totalBytesUploaded += int64(chunkSize) // We successfully sent one full chunk
				totalBytesUploadedMutex.Unlock()
			}
		}(srv)
	}

	wg.Wait()
	close(errorsChan)

	for err := range errorsChan {
		log.Printf("Upload stream error: %v\n", err)
	}

	if testDuration.Seconds() == 0 || totalBytesUploaded == 0 {
		return 0, fmt.Errorf("upload test yielded no data or test duration was zero")
	}

	speedMbps := (float64(totalBytesUploaded) * 8) / (testDuration.Seconds() * 1000000)
	return speedMbps, nil
}

func main() {
	log.SetFlags(0) // Simpler logging output

	fmt.Println("Fetching server list...")
	initialTargets, err := fetchTestServers()
	if err != nil || len(initialTargets) == 0 {
		log.Fatalf("Error fetching test servers: %v", err)
	}
	fmt.Printf("Found %d potential servers from API.\n", len(initialTargets))

	fmt.Println("Pinging servers to select the best ones...")
	pingedTargets := measurePings(initialTargets)

	if len(pingedTargets) == 0 {
		log.Fatalf("No servers responded to ping successfully.")
	}

	numToUse := numServersToTest
	if len(pingedTargets) < numToUse {
		numToUse = len(pingedTargets)
		fmt.Printf("Warning: Fewer than %d responsive servers available, using %d.\n", numServersToTest, numToUse)
	}
	if numToUse == 0 {
		log.Fatalf("No responsive servers to test with.")
	}

	selectedPingedTargets := pingedTargets[:numToUse]
	var selectedTargetsForTest []target
	var totalPingLatency time.Duration

	fmt.Println("\nSelected servers for speed tests:")
	for _, pt := range selectedPingedTargets {
		fmt.Printf("  - %s (%s, %s) - Latency: %v\n", pt.Target.Name, pt.Target.Location.City, pt.Target.Location.Country, pt.Latency.Round(time.Millisecond))
		selectedTargetsForTest = append(selectedTargetsForTest, pt.Target)
		totalPingLatency += pt.Latency
	}

	avgPingStr := "N/A"
	if numToUse > 0 {
		avgPing := totalPingLatency / time.Duration(numToUse)
		avgPingStr = avgPing.Round(time.Millisecond).String()
	}

	// Perform Download Test
	fmt.Printf("\nPerforming download test...\n")
	
	downloadSpeedMbps, err := performDownloadTest(selectedTargetsForTest, downloadTestDuration, downloadChunkSizeBytes)
	if err != nil {
		log.Printf("Download test error: %v. Reported speed might be affected.", err)
	}

	// Perform Upload Test
	fmt.Printf("\nPerforming upload test...\n")
	uploadSpeedMbps, err := performUploadTest(selectedTargetsForTest, uploadTestDuration, uploadChunkSizeBytes)
	if err != nil {
		log.Printf("Upload test error: %v. Reported speed might be affected.", err)
	}

	// Output Results
	fmt.Println("\n--- Speed Test Results ---")
	fmt.Printf("Average Ping to selected servers: %s\n", avgPingStr)
	fmt.Printf("Download Speed: %.2f Mbps\n", downloadSpeedMbps)
	fmt.Printf("Upload Speed: %.2f Mbps\n", uploadSpeedMbps)
}
