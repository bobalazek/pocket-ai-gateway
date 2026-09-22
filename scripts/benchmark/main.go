// Benchmark runs a real standalone gateway against a loopback mock. It never
// accepts an existing data directory or an external provider address.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const mockDelay = 10 * time.Millisecond

var client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 100}}
var sequence atomic.Int64
var timings sync.Map
var active, peakActive atomic.Int64

type observation struct {
	TotalMS, FirstByteMS, AddedMS, LaunchLagMS float64
	Error                                      string `json:",omitempty"`
}

type statistics struct {
	Count, Errors              int
	P50MS, P95MS, P99MS, MaxMS float64
}

func main() {
	binary := flag.String("binary", "dist/pocket-ai-gateway", "existing standalone binary")
	duration := flag.Duration("duration", time.Minute, "duration of the JSON workload")
	streamDuration := flag.Duration("stream-duration", 0, "stream workload duration; defaults to -duration")
	cooldown := flag.Duration("cooldown", 2*time.Minute, "idle period before final resource sample; 0 skips waiting")
	flag.Parse()
	if *streamDuration == 0 {
		*streamDuration = *duration
	}
	if *duration < time.Second || *streamDuration < time.Second || *cooldown < 0 {
		fmt.Fprintln(os.Stderr, "workload durations must be at least 1s and cooldown cannot be negative")
		os.Exit(2)
	}
	if err := run(*binary, *duration, *streamDuration, *cooldown); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(binary string, duration, streamDuration, cooldown time.Duration) error {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	artifact, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "pocket-ai-gateway-benchmark-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	upstream := httptest.NewServer(http.HandlerFunc(mock))
	defer upstream.Close()
	dataDir := filepath.Join(dir, "data")
	session, secret, sqlite, err := seed(dataDir, upstream.URL)
	if err != nil {
		return err
	}
	report := map[string]any{
		"timestamp_utc": time.Now().UTC().Format(time.RFC3339), "artifact_sha256": fmt.Sprintf("%x", sha256.Sum256(artifact)),
		"binary_build": info, "sqlite_version": sqlite, "harness_go_version": runtime.Version(),
		"source_revision": command("git", "rev-parse", "HEAD"), "source_worktree": command("git", "status", "--short"),
		"os": command("uname", "-a"), "cpu_count": runtime.NumCPU(), "storage": command("df", "-h", dir),
		"json_duration_seconds": duration.Seconds(), "stream_duration_seconds": streamDuration.Seconds(), "cooldown_seconds": cooldown.Seconds(),
		"json_requests_per_second": 50, "request_body_bytes": 1024,
		"resource_sample_interval_ms": 200,
		"stream_concurrency":          50, "mock_delay_ms": 10, "stream_chunks": 20, "stream_chunk_interval_ms": 100,
		"initial_database_bytes": databaseSizes(dataDir),
	}
	if runtime.GOOS == "darwin" {
		report["cpu"] = command("sysctl", "-n", "machdep.cpu.brand_string")
		report["memory_bytes"] = command("sysctl", "-n", "hw.memsize")
		report["os_version"] = command("sw_vers")
	} else {
		report["cpu"] = command("lscpu")
		report["memory"] = command("free", "-b")
	}
	var starts []float64
	var process *exec.Cmd
	var base string
	for range 5 {
		process, base, err = start(binary, dataDir, dir)
		if err != nil {
			return err
		}
		starts = append(starts, startupMS)
		if len(starts) < 5 {
			if err := stop(process); err != nil {
				return err
			}
		}
	}
	defer stop(process) // also clean up when a probe fails
	report["startup_ms"] = summarize(starts, 0)
	time.Sleep(2 * time.Second)
	idle, _ := resources(process.Process.Pid)
	if idle <= 0 {
		return fmt.Errorf("could not measure standalone process RSS with ps")
	}
	report["idle_rss_mib"] = idle
	diagnostic, err := getJSON(base+"/api/v1/admin/diagnostics", session)
	if err != nil {
		return err
	}
	idleGoroutines, err := goroutineCount(diagnostic)
	if err != nil {
		return err
	}
	report["idle_goroutines"] = idleGoroutines
	var rssPeak, cpuPeak float64
	goroutinePeak := idleGoroutines
	var pendingPeak float64
	var management []observation
	var streamWindows []resourceWindow
	var streamStart time.Time
	var samples int
	var sampleMu sync.Mutex
	done := make(chan struct{})
	var sampler sync.WaitGroup
	stopSampler := sync.OnceFunc(func() { close(done); sampler.Wait() })
	defer stopSampler()
	sampler.Go(func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			rss, cpu := resources(process.Process.Pid)
			t0 := time.Now()
			diagnostic, e := getJSON(base+"/api/v1/admin/diagnostics", session)
			var goroutines int
			if e == nil {
				goroutines, e = goroutineCount(diagnostic)
			}
			if e == nil && rss <= 0 {
				e = fmt.Errorf("could not measure standalone process RSS with ps")
			}
			sampleMu.Lock()
			rssPeak = math.Max(rssPeak, rss)
			cpuPeak = math.Max(cpuPeak, cpu)
			samples++
			item := observation{TotalMS: milliseconds(time.Since(t0))}
			if e != nil {
				item.Error = e.Error()
			} else if value, ok := diagnostic["diagnostics"].(map[string]any); ok {
				pendingPeak = math.Max(pendingPeak, value["pending_outbox_events"].(float64))
				goroutinePeak = max(goroutinePeak, goroutines)
				if !streamStart.IsZero() && !t0.Before(streamStart) {
					streamWindows = addResourceSample(streamWindows, t0.Sub(streamStart), rss, goroutines)
				}
			}
			management = append(management, item)
			sampleMu.Unlock()
		}
	})
	fmt.Fprintln(os.Stderr, "Measuring 50 JSON requests/second...")
	jsonResults, actualRate, maxInFlight := rateLoad(base+"/api/openai/v1/chat/completions", secret, duration)
	report["json"] = results(jsonResults)
	report["json_achieved_requests_per_second"] = actualRate
	report["json_peak_client_inflight"] = maxInFlight
	fmt.Fprintln(os.Stderr, "Measuring 50 concurrent streams...")
	peakActive.Store(0)
	sampleMu.Lock()
	streamManagementStart := len(management)
	streamStart = time.Now()
	sampleMu.Unlock()
	var streams []observation
	var streamMu sync.Mutex
	var workers sync.WaitGroup
	for range 50 {
		workers.Go(func() {
			for time.Since(streamStart) < streamDuration {
				item := infer(base+"/api/openai/v1/chat/completions", secret, true)
				streamMu.Lock()
				streams = append(streams, item)
				streamMu.Unlock()
			}
		})
	}
	workers.Wait()
	report["streams"] = results(streams)
	report["stream_peak_mock_inflight"] = peakActive.Load()
	report["stream_actual_duration_seconds"] = time.Since(streamStart).Seconds()
	stopSampler()
	report["management_during_load"] = results(management)
	report["management_during_streams"] = results(management[streamManagementStart:])
	report["sampled_peak_rss_mib"] = rssPeak
	report["sampled_peak_ps_cpu_percent"] = cpuPeak
	report["sampled_peak_goroutines"] = goroutinePeak
	report["stream_resource_minute_windows"] = streamWindows
	report["resource_samples"] = samples
	report["sampled_pending_projection_peak"] = pendingPeak
	drainStart := time.Now()
	for {
		value, e := getJSON(base+"/api/v1/admin/diagnostics", session)
		if e != nil {
			return e
		}
		if value["diagnostics"].(map[string]any)["pending_outbox_events"].(float64) == 0 {
			break
		}
		if time.Since(drainStart) > 30*time.Second {
			return fmt.Errorf("projection did not drain within 30 seconds")
		}
		time.Sleep(100 * time.Millisecond)
	}
	report["projection_drain_ms"] = milliseconds(time.Since(drainStart))
	report["final_database_bytes_including_wal"] = databaseSizes(dataDir)
	report["usage"], err = getJSON(base+"/api/v1/usage", session)
	if err != nil {
		return err
	}
	usageSummary := report["usage"].(map[string]any)["usage"].(map[string]any)
	expected := len(jsonResults) + len(streams)
	if usageSummary["requests"].(float64) != float64(expected) || usageSummary["unknown_attempts"].(float64) != 0 {
		return fmt.Errorf("usage did not settle all %d benchmark requests", expected)
	}
	client.CloseIdleConnections()
	fmt.Fprintf(os.Stderr, "Cooling down for %s...\n", cooldown)
	time.Sleep(cooldown)
	finalRSS, _ := resources(process.Process.Pid)
	if finalRSS <= 0 {
		return fmt.Errorf("could not measure final standalone process RSS with ps")
	}
	diagnostic, err = getJSON(base+"/api/v1/admin/diagnostics", session)
	if err != nil {
		return err
	}
	finalGoroutines, err := goroutineCount(diagnostic)
	if err != nil {
		return err
	}
	report["cooldown_rss_mib"] = finalRSS
	report["cooldown_goroutines"] = finalGoroutines
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	for _, group := range [][]observation{jsonResults, streams, management} {
		for _, item := range group {
			if item.Error != "" {
				return fmt.Errorf("benchmark request failed; see JSON error counts: %s", item.Error)
			}
		}
	}
	return stop(process)
}

var startupMS float64
var listenPattern = regexp.MustCompile(`listen=([^ ]+)`)

func start(binary, dataDir, dir string) (*exec.Cmd, string, error) {
	logPath := filepath.Join(dir, "gateway.log")
	log, err := os.Create(logPath)
	if err != nil {
		return nil, "", err
	}
	defer log.Close()
	cmd := exec.Command(binary, "serve", "--listen", "127.0.0.1:0", "--data-dir", dataDir)
	cmd.Dir = dir
	// Drop operator gateway settings, external credentials, and proxy settings.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdout, cmd.Stderr = log, log
	started := time.Now()
	if err = cmd.Start(); err != nil {
		return nil, "", err
	}
	for time.Since(started) < 10*time.Second {
		output, _ := os.ReadFile(logPath)
		match := listenPattern.FindSubmatch(output)
		if len(match) == 2 {
			base := "http://" + string(match[1])
			response, e := client.Get(base + "/readyz")
			if e == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					startupMS = milliseconds(time.Since(started))
					return cmd, base, nil
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	_ = stop(cmd)
	return nil, "", fmt.Errorf("standalone gateway did not become ready")
}

func stop(cmd *exec.Cmd) error {
	if cmd.ProcessState != nil {
		return nil
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		return err
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-finished
		return fmt.Errorf("gateway did not stop within 10 seconds; killed and reaped")
	}
}

func mock(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 1 || len(request.Messages[0].Content) < 12 {
		http.Error(w, "invalid benchmark request", http.StatusBadRequest)
		return
	}
	id := request.Messages[0].Content[:12]
	arrived := time.Now()
	inflight := active.Add(1)
	defer active.Add(-1)
	for old := peakActive.Load(); inflight > old; old = peakActive.Load() {
		if peakActive.CompareAndSwap(old, inflight) {
			break
		}
	}
	time.Sleep(mockDelay)
	timings.Store(id, milliseconds(time.Since(arrived)))
	if !request.Stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"object":"chat.completion","model":"benchmark","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":256,"completion_tokens":1,"total_tokens":257}}`, id)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for index := range 20 {
		fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":\"benchmark\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n", id)
		w.(http.Flusher).Flush()
		if index < 19 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":\"benchmark\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":256,\"completion_tokens\":20,\"total_tokens\":276}}\n\ndata: [DONE]\n\n", id)
}

func infer(url, secret string, stream bool) observation {
	id := fmt.Sprintf("%012d", sequence.Add(1))
	body := fmt.Sprintf(`{"model":"benchmark","messages":[{"role":"user","content":"%s"}],"max_tokens":32,"stream":%t}`, id, stream)
	body = strings.Replace(body, id, id+strings.Repeat("x", 1024-len(body)), 1)
	request, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	started := time.Now()
	response, err := client.Do(request)
	item := observation{}
	if err == nil {
		defer response.Body.Close()
		var data []byte
		if stream {
			reader := bufio.NewReader(response.Body)
			var first []byte
			first, err = reader.ReadBytes('\n')
			item.FirstByteMS = milliseconds(time.Since(started))
			if err == nil {
				data, err = io.ReadAll(reader)
				data = append(first, data...)
			}
		} else {
			data, err = io.ReadAll(response.Body)
			item.FirstByteMS = milliseconds(time.Since(started))
		}
		if response.StatusCode != 200 {
			err = fmt.Errorf("HTTP %d: %.200s", response.StatusCode, data)
		}
		if err == nil && !bytes.Contains(data, []byte(`"id":"`+id+`"`)) {
			err = fmt.Errorf("missing matching response ID")
		}
		if err == nil && stream && !bytes.Contains(data, []byte("data: [DONE]")) {
			err = fmt.Errorf("stream missing terminal event")
		}
		if err == nil && !bytes.Contains(data, []byte(`"usage"`)) {
			err = fmt.Errorf("missing usage")
		}
	}
	item.TotalMS = milliseconds(time.Since(started))
	if delay, ok := timings.LoadAndDelete(id); ok {
		item.AddedMS = item.FirstByteMS - delay.(float64)
	}
	if err != nil {
		item.Error = err.Error()
	}
	return item
}

func rateLoad(url, secret string, duration time.Duration) ([]observation, float64, int64) {
	var mu sync.Mutex
	var items []observation
	var workers sync.WaitGroup
	var inflight, peak atomic.Int64
	started := time.Now()
	count := int(duration / (20 * time.Millisecond))
	for index := range count {
		target := started.Add(time.Duration(index) * 20 * time.Millisecond)
		time.Sleep(time.Until(target))
		lag := milliseconds(time.Since(target))
		workers.Go(func() {
			n := inflight.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			item := infer(url, secret, false)
			inflight.Add(-1)
			item.LaunchLagMS = lag
			mu.Lock()
			items = append(items, item)
			mu.Unlock()
		})
	}
	workers.Wait()
	return items, float64(count) / time.Since(started).Seconds(), peak.Load()
}

func results(items []observation) map[string]any {
	var total, first, added, lag []float64
	errors := map[string]int{}
	for _, item := range items {
		if item.Error != "" {
			errors[item.Error]++
			continue
		}
		total = append(total, item.TotalMS)
		first = append(first, item.FirstByteMS)
		added = append(added, item.AddedMS)
		lag = append(lag, item.LaunchLagMS)
	}
	count := len(items) - len(total)
	return map[string]any{"total": summarize(total, count), "response_ready": summarize(first, count), "added_gateway_ms": summarize(added, count), "launch_lag": summarize(lag, count), "errors": errors}
}

func summarize(values []float64, errors int) statistics {
	sort.Float64s(values)
	value := func(p float64) float64 {
		if len(values) == 0 {
			return 0
		}
		return values[int(math.Ceil(p*float64(len(values))))-1]
	}
	return statistics{Count: len(values) + errors, Errors: errors, P50MS: value(.50), P95MS: value(.95), P99MS: value(.99), MaxMS: value(1)}
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func command(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}
func resources(pid int) (rssMiB, cpu float64) {
	fields := strings.Fields(command("ps", "-o", "rss=,pcpu=", "-p", strconv.Itoa(pid)))
	if len(fields) == 2 {
		rssMiB, _ = strconv.ParseFloat(fields[0], 64)
		cpu, _ = strconv.ParseFloat(fields[1], 64)
	}
	return rssMiB / 1024, cpu
}
func databaseSizes(dir string) map[string]int64 {
	result := map[string]int64{}
	for _, name := range []string{"system.db", "system.db-wal", "system.db-shm", "data.db", "data.db-wal", "data.db-shm"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			result[name] = info.Size()
		}
	}
	return result
}
func getJSON(url, session string) (map[string]any, error) {
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	request.AddCookie(&http.Cookie{Name: authSessionCookie, Value: session})
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("management HTTP %d", response.StatusCode)
	}
	var result map[string]any
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

const authSessionCookie = "pocket_ai_gateway_session"
