package main

import (
	"testing"
	"time"
)

func TestGoroutinesRequireMeasurement(t *testing.T) {
	for _, input := range []map[string]any{nil, {"diagnostics": map[string]any{}}, {"diagnostics": map[string]any{"goroutines": 0.0}}, {"diagnostics": map[string]any{"goroutines": 2.5}}} {
		if _, err := goroutineCount(input); err == nil {
			t.Fatal("accepted unavailable or invalid goroutine measurement")
		}
	}
	if count, err := goroutineCount(map[string]any{"diagnostics": map[string]any{"goroutines": 42.0}}); err != nil || count != 42 {
		t.Fatalf("got %d, %v", count, err)
	}
}

func TestResourceMinuteWindows(t *testing.T) {
	windows := addResourceSample(nil, 200*time.Millisecond, 40, 60)
	windows = addResourceSample(windows, 59*time.Second, 35, 65)
	windows = addResourceSample(windows, time.Minute, 30, 50)
	if len(windows) != 2 || windows[0].Samples != 2 || windows[0].RSSMinMiB != 35 || windows[0].RSSMaxMiB != 40 || windows[0].GoroutinesMin != 60 || windows[0].GoroutinesMax != 65 || windows[1].Minute != 2 || windows[1].FirstSampleSeconds != 60 || windows[1].GoroutinesMin != 50 {
		t.Fatalf("incorrect resource windows: %+v", windows)
	}
}
