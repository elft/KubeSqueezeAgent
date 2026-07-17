package prometheus

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/golang/snappy"
)

type seedProfile struct {
	namespace string
	workload  string
	pattern   string
	base      float64
	peak      float64
}

var seedProfiles = []seedProfile{
	{"preview-pr-1842", "catalog-preview", "abandoned", 0.008, 0.02},
	{"preview-pr-1901", "search-preview", "bursty", 0.03, 0.18},
	{"development", "checkout-api", "office-hours", 0.18, 0.62},
	{"development", "recommendation-api", "steady", 0.22, 0.48},
	{"development", "rollback-demo", "office-hours", 0.12, 0.42},
	{"development", "payment-simulator", "steady", 0.34, 0.7},
	{"customer-demos", "sales-demo", "bursty", 0.08, 0.45},
	{"production", "payments-api", "production", 1.3, 2.8},
	{"production", "orders-api", "production", 0.9, 2.1},
}

func SeedHistory(ctx context.Context, baseURL string) error {
	now := time.Now().UTC().Truncate(time.Minute)
	series := make([][]byte, 0, len(seedProfiles))
	for profileIndex, profile := range seedProfiles {
		labels := map[string]string{
			"__name__": "demo_workload_cpu_cores", "namespace": profile.namespace,
			"workload": profile.workload, "pattern": profile.pattern, "source": "seed",
		}
		var samples []sample
		for at := now.Add(-7 * 24 * time.Hour); at.Before(now.Add(-2 * time.Minute)); at = at.Add(15 * time.Minute) {
			value := seededValue(profile, at, profileIndex)
			samples = append(samples, sample{value: value, timestamp: at.UnixMilli()})
		}
		series = append(series, encodeTimeSeries(labels, samples))
	}

	var requestBody []byte
	for _, encoded := range series {
		requestBody = appendMessageField(requestBody, 1, encoded)
	}
	compressed := snappy.Encode(nil, requestBody)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/write", bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Encoding", "snappy")
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("remote-write seed returned %s", response.Status)
	}
	return nil
}

type sample struct {
	value     float64
	timestamp int64
}

func seededValue(profile seedProfile, at time.Time, offset int) float64 {
	hour := float64(at.Hour()) + float64(at.Minute())/60
	daily := (math.Sin((hour-7)/24*2*math.Pi) + 1) / 2
	weekday := at.Weekday() != time.Saturday && at.Weekday() != time.Sunday
	noise := (math.Sin(float64(at.Unix()/900+int64(offset*17))) + 1) * 0.025
	factor := daily
	switch profile.pattern {
	case "abandoned":
		factor = 0.05
	case "office-hours":
		if !weekday || hour < 7 || hour > 19 {
			factor = 0.08
		} else {
			factor = 0.35 + daily*0.65
		}
	case "bursty":
		factor = 0.15 + math.Pow(daily, 8)*0.85
	case "steady":
		factor = 0.55 + daily*0.25
	case "production":
		factor = 0.45 + daily*0.55
	}
	return profile.base + (profile.peak-profile.base)*factor + noise
}

func encodeTimeSeries(labels map[string]string, samples []sample) []byte {
	var body []byte
	keys := make([]string, 0, len(labels))
	for name := range labels {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := labels[name]
		var label []byte
		label = appendStringField(label, 1, name)
		label = appendStringField(label, 2, value)
		body = appendMessageField(body, 1, label)
	}
	for _, item := range samples {
		var encoded []byte
		encoded = append(encoded, byte(1<<3|1))
		bits := make([]byte, 8)
		binary.LittleEndian.PutUint64(bits, math.Float64bits(item.value))
		encoded = append(encoded, bits...)
		encoded = appendVarintField(encoded, 2, uint64(item.timestamp))
		body = appendMessageField(body, 2, encoded)
	}
	return body
}

func appendStringField(dst []byte, field int, value string) []byte {
	return appendMessageField(dst, field, []byte(value))
}

func appendMessageField(dst []byte, field int, value []byte) []byte {
	dst = appendUvarint(dst, uint64(field<<3|2))
	dst = appendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendVarintField(dst []byte, field int, value uint64) []byte {
	dst = appendUvarint(dst, uint64(field<<3))
	return appendUvarint(dst, value)
}

func appendUvarint(dst []byte, value uint64) []byte {
	var buffer [10]byte
	n := binary.PutUvarint(buffer[:], value)
	return append(dst, buffer[:n]...)
}
