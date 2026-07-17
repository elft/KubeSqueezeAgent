package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) P95CPU(ctx context.Context, namespace, workload string) (*float64, float64) {
	selector := fmt.Sprintf(`demo_workload_cpu_cores{namespace=%q,workload=%q}`, namespace, workload)
	values, err := c.instant(ctx, "max(quantile_over_time(0.95, "+selector+"[7d]))")
	if err != nil || len(values) == 0 {
		return nil, 0
	}
	counts, err := c.instant(ctx, "max(count_over_time("+selector+"[7d]))")
	if err != nil || len(counts) == 0 {
		return nil, 0
	}
	// The synthetic backfill uses a 15-minute interval (672 expected samples).
	// Real clusters normally scrape more often, so cap denser data at full coverage.
	coverage := counts[0] / 672
	if coverage > 1 {
		coverage = 1
	}
	value := values[0]
	return &value, coverage
}

func (c *Client) History(ctx context.Context, namespace, workload string, duration time.Duration) ([]domain.ChartPoint, error) {
	query := fmt.Sprintf(`max(demo_workload_cpu_cores{namespace=%q,workload=%q})`, namespace, workload)
	end := time.Now()
	start := end.Add(-duration)
	params := url.Values{
		"query": []string{query}, "start": []string{strconv.FormatInt(start.Unix(), 10)},
		"end": []string{strconv.FormatInt(end.Unix(), 10)}, "step": []string{"15m"},
	}
	var response apiResponse
	if err := c.get(ctx, "/api/v1/query_range", params, &response); err != nil {
		return nil, err
	}
	if len(response.Data.Result) == 0 {
		return []domain.ChartPoint{}, nil
	}
	points := make([]domain.ChartPoint, 0, len(response.Data.Result[0].Values))
	for _, item := range response.Data.Result[0].Values {
		if len(item) != 2 {
			continue
		}
		ts, ok := asFloat(item[0])
		if !ok {
			continue
		}
		value, ok := asFloat(item[1])
		if ok {
			points = append(points, domain.ChartPoint{Timestamp: int64(ts), Value: value})
		}
	}
	return points, nil
}

func (c *Client) Ready(ctx context.Context) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/-/ready", nil)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus readiness returned %s", response.Status)
	}
	return nil
}

func (c *Client) instant(ctx context.Context, query string) ([]float64, error) {
	var response apiResponse
	if err := c.get(ctx, "/api/v1/query", url.Values{"query": []string{query}}, &response); err != nil {
		return nil, err
	}
	var values []float64
	for _, result := range response.Data.Result {
		if len(result.Value) == 2 {
			if value, ok := asFloat(result.Value[1]); ok {
				values = append(values, value)
			}
		}
	}
	return values, nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus returned %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

type apiResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value  []any   `json:"value"`
			Values [][]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
