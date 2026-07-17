package prometheus

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestP95CPUComputesSevenDayCoverage(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query().Get("query")
		value := "0.62"
		if strings.Contains(query, "count_over_time") {
			value = "336"
		}
		body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"` + value + `"]}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	client := &Client{baseURL: "http://prometheus.test", http: httpClient}

	value, coverage := client.P95CPU(context.Background(), "development", "checkout-api")
	if value == nil || *value != 0.62 {
		t.Fatalf("P95CPU() value = %v; want 0.62", value)
	}
	if coverage != 0.5 {
		t.Fatalf("P95CPU() coverage = %v; want 0.5", coverage)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
