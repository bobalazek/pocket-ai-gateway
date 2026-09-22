package main

import (
	"fmt"
	"math"
	"time"
)

type resourceWindow struct {
	Minute             int     `json:"minute"`
	Samples            int     `json:"samples"`
	FirstSampleSeconds float64 `json:"first_sample_seconds"`
	LastSampleSeconds  float64 `json:"last_sample_seconds"`
	RSSMinMiB          float64 `json:"rss_min_mib"`
	RSSMaxMiB          float64 `json:"rss_max_mib"`
	GoroutinesMin      int     `json:"goroutines_min"`
	GoroutinesMax      int     `json:"goroutines_max"`
}

func addResourceSample(windows []resourceWindow, elapsed time.Duration, rss float64, goroutines int) []resourceWindow {
	minute := int(elapsed/time.Minute) + 1
	if len(windows) == 0 || windows[len(windows)-1].Minute != minute {
		windows = append(windows, resourceWindow{Minute: minute, FirstSampleSeconds: elapsed.Seconds(), RSSMinMiB: rss, GoroutinesMin: goroutines})
	}
	window := &windows[len(windows)-1]
	window.Samples++
	window.LastSampleSeconds = elapsed.Seconds()
	window.RSSMinMiB = min(window.RSSMinMiB, rss)
	window.RSSMaxMiB = max(window.RSSMaxMiB, rss)
	window.GoroutinesMin = min(window.GoroutinesMin, goroutines)
	window.GoroutinesMax = max(window.GoroutinesMax, goroutines)
	return windows
}

func goroutineCount(response map[string]any) (int, error) {
	diagnostic, ok := response["diagnostics"].(map[string]any)
	if ok {
		value, valid := diagnostic["goroutines"].(float64)
		if valid && value >= 1 && value < math.MaxInt32 && value == math.Trunc(value) {
			return int(value), nil
		}
	}
	return 0, fmt.Errorf("diagnostics must include a positive integer goroutines measurement; rebuild the gateway binary")
}
