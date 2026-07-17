package workload

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

func Run(ctx context.Context) error {
	name := env("WORKLOAD_NAME", "demo-workload")
	namespace := env("WORKLOAD_NAMESPACE", "development")
	pattern := env("UTILIZATION_PATTERN", "office-hours")
	base := floatEnv("BASE_CPU_CORES", 0.08)
	peak := floatEnv("PEAK_CPU_CORES", 0.5)
	started := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "KubeSqueeze demo workload %s/%s\n", namespace, name)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		value := utilization(pattern, base, peak, time.Now(), started)
		labels := fmt.Sprintf(`namespace=%q,workload=%q,pattern=%q`, namespace, name, pattern)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP demo_workload_cpu_cores Synthetic workload CPU demand in cores.\n")
		fmt.Fprintf(w, "# TYPE demo_workload_cpu_cores gauge\n")
		fmt.Fprintf(w, "demo_workload_cpu_cores{%s} %.6f\n", labels, value)
		fmt.Fprintf(w, "# HELP demo_workload_health Synthetic workload health.\n")
		fmt.Fprintf(w, "# TYPE demo_workload_health gauge\n")
		fmt.Fprintf(w, "demo_workload_health{%s} 1\n", labels)
	})

	server := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func utilization(pattern string, base, peak float64, now, started time.Time) float64 {
	hour := float64(now.Hour()) + float64(now.Minute())/60
	daily := (math.Sin((hour-7)/24*2*math.Pi) + 1) / 2
	pulse := (math.Sin(time.Since(started).Seconds()/20) + 1) / 2
	factor := daily
	switch strings.ToLower(pattern) {
	case "abandoned":
		factor = 0.03
	case "bursty":
		factor = 0.1 + math.Pow(pulse, 10)*0.9
	case "steady":
		factor = 0.55 + pulse*0.1
	case "production":
		factor = 0.45 + daily*0.4 + pulse*0.15
	case "office-hours":
		if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday || hour < 7 || hour > 19 {
			factor = 0.06
		} else {
			factor = 0.3 + daily*0.6 + pulse*0.1
		}
	}
	return base + (peak-base)*factor
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func floatEnv(key string, fallback float64) float64 {
	var value float64
	if _, err := fmt.Sscan(os.Getenv(key), &value); err == nil {
		return value
	}
	return fallback
}
