package config

import "testing"

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		valid  bool
	}{
		{"defaults", map[string]string{"BACKENDS": "http://localhost:8081"}, true},
		{"missing_backends", nil, false},
		{"invalid_duration", map[string]string{"BACKENDS": "http://localhost:8081", "UPSTREAM_TIMEOUT": "0s"}, false},
		{"invalid_rate", map[string]string{"BACKENDS": "http://localhost:8081", "RATE_LIMIT_RPS": "NaN"}, false},
		{"invalid_strategy", map[string]string{"BACKENDS": "http://localhost:8081", "BALANCER_STRATEGY": "random"}, false},
		{"invalid_proxy", map[string]string{"BACKENDS": "http://localhost:8081", "TRUSTED_PROXY_CIDRS": "all"}, false},
		{"duplicate_ports", map[string]string{"BACKENDS": "http://localhost:8081", "ADMIN_ADDR": ":8080"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(func(key string) (string, bool) { v, ok := tc.values[key]; return v, ok })
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
		})
	}
}
