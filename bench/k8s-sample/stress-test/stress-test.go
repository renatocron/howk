// Stress test tool for HOWK webhook system
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Stats struct {
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	LatencySum      int64
	MinLatency      int64
	MaxLatency      int64
}

func (s *Stats) Record(latency time.Duration, success bool) {
	atomic.AddInt64(&s.TotalRequests, 1)
	ms := latency.Milliseconds()
	atomic.AddInt64(&s.LatencySum, ms)

	if success {
		atomic.AddInt64(&s.SuccessRequests, 1)
	} else {
		atomic.AddInt64(&s.FailedRequests, 1)
	}

	for {
		old := atomic.LoadInt64(&s.MinLatency)
		if old == 0 || ms < old {
			if atomic.CompareAndSwapInt64(&s.MinLatency, old, ms) {
				break
			}
		} else {
			break
		}
	}

	for {
		old := atomic.LoadInt64(&s.MaxLatency)
		if ms > old {
			if atomic.CompareAndSwapInt64(&s.MaxLatency, old, ms) {
				break
			}
		} else {
			break
		}
	}
}

func (s *Stats) Print(duration time.Duration) {
	total := atomic.LoadInt64(&s.TotalRequests)
	success := atomic.LoadInt64(&s.SuccessRequests)
	failed := atomic.LoadInt64(&s.FailedRequests)
	latencySum := atomic.LoadInt64(&s.LatencySum)
	minLatency := atomic.LoadInt64(&s.MinLatency)
	maxLatency := atomic.LoadInt64(&s.MaxLatency)

	fmt.Println("\n========== Stress Test Results ==========")
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Total Requests: %d\n", total)
	fmt.Printf("Successful: %d (%.2f%%)\n", success, float64(success)/float64(total)*100)
	fmt.Printf("Failed: %d (%.2f%%)\n", failed, float64(failed)/float64(total)*100)
	fmt.Printf("Throughput: %.2f req/sec\n", float64(total)/duration.Seconds())
	if total > 0 {
		fmt.Printf("Average Latency: %d ms\n", latencySum/total)
		fmt.Printf("Min Latency: %d ms\n", minLatency)
		fmt.Printf("Max Latency: %d ms\n", maxLatency)
	}
	fmt.Println("=========================================")
}

type WebhookRequest struct {
	Endpoint string          `json:"endpoint"`
	Payload  json.RawMessage `json:"payload"`
}

func worker(
	apiURL string,
	configID string,
	endpoint string,
	ratePerSec int,
	duration time.Duration,
	stats *Stats,
	wg *sync.WaitGroup,
	stopChan <-chan struct{},
) {
	defer wg.Done()

	ticker := time.NewTicker(time.Second / time.Duration(ratePerSec))
	defer ticker.Stop()

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"event": "stress.test",
		"timestamp": time.Now().Unix(),
		"data": map[string]interface{}{
			"load_test": true,
			"random": time.Now().Nanosecond(),
		},
	})

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			reqBody, _ := json.Marshal(WebhookRequest{
				Endpoint: endpoint,
				Payload:  payload,
			})

			url := fmt.Sprintf("%s/webhooks/%s/enqueue", apiURL, configID)
			start := time.Now()
			resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
			latency := time.Since(start)

			if err != nil {
				stats.Record(latency, false)
				continue
			}
			resp.Body.Close()

			success := resp.StatusCode == http.StatusAccepted
			stats.Record(latency, success)
		}
	}
}

func main() {
	var (
		apiURL     = flag.String("api", "http://api:8080", "API base URL")
		configID   = flag.String("config", "stress-test", "Config ID for webhooks")
		endpoint   = flag.String("endpoint", "http://echo-server:8080/webhook", "Target webhook endpoint")
		workers    = flag.Int("workers", 10, "Number of concurrent workers")
		ratePerSec = flag.Int("rate", 10, "Requests per second per worker")
		duration   = flag.Duration("duration", 60*time.Second, "Test duration")
	)
	flag.Parse()

	fmt.Println("HOWK Stress Test Tool")
	fmt.Println("=====================")
	fmt.Printf("API URL: %s\n", *apiURL)
	fmt.Printf("Config ID: %s\n", *configID)
	fmt.Printf("Target Endpoint: %s\n", *endpoint)
	fmt.Printf("Workers: %d\n", *workers)
	fmt.Printf("Rate per worker: %d req/sec\n", *ratePerSec)
	fmt.Printf("Total rate: %d req/sec\n", *workers**ratePerSec)
	fmt.Printf("Duration: %v\n", *duration)
	fmt.Println()

	stats := &Stats{}
	stopChan := make(chan struct{})
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(*apiURL, *configID, *endpoint, *ratePerSec, *duration, stats, &wg, stopChan)
	}

	// Start progress reporter
	progressTicker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-progressTicker.C:
				total := atomic.LoadInt64(&stats.TotalRequests)
				success := atomic.LoadInt64(&stats.SuccessRequests)
				failed := atomic.LoadInt64(&stats.FailedRequests)
				fmt.Printf("Progress: %d total, %d success, %d failed\n", total, success, failed)
			case <-stopChan:
				return
			}
		}
	}()

	// Wait for test duration
	time.Sleep(*duration)
	close(stopChan)
	wg.Wait()
	progressTicker.Stop()

	// Print results
	stats.Print(*duration)

	// Exit with error code if too many failures
	if stats.TotalRequests == 0 || float64(stats.FailedRequests)/float64(stats.TotalRequests) > 0.1 {
		fmt.Println("ERROR: Too many failures!")
		os.Exit(1)
	}
}
