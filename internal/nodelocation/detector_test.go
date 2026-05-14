package nodelocation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
)

func TestDetectorUsesManualLocationBeforeCloudOrIPInfo(t *testing.T) {
	latitude := 41.0082
	longitude := 28.9784
	cloudCalls := 0

	location, err := Detector{
		Config: config.Config{
			LocationManualRegion:    "Istanbul Home Lab",
			LocationManualZone:      "Rack 1",
			LocationManualLatitude:  &latitude,
			LocationManualLongitude: &longitude,
			LocationPublicIPEnabled: true,
		},
		CloudDetect: func(context.Context) (*api.NodeLocation, error) {
			cloudCalls++
			return nil, errors.New("cloud should not be called")
		},
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if cloudCalls != 0 {
		t.Fatalf("cloud detector calls = %d, want 0", cloudCalls)
	}
	if location == nil || location.Provider != "manual" || location.Source != "manual" {
		t.Fatalf("unexpected location: %+v", location)
	}
	if location.Latitude == nil || *location.Latitude != latitude {
		t.Fatalf("latitude = %v, want %v", location.Latitude, latitude)
	}
	if location.Longitude == nil || *location.Longitude != longitude {
		t.Fatalf("longitude = %v, want %v", location.Longitude, longitude)
	}
}

func TestDetectorUsesCloudBeforeIPInfo(t *testing.T) {
	ipInfoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ipInfoCalls++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"city": "Istanbul",
			"loc":  "41.0082,28.9784",
		})
	}))
	defer server.Close()

	location, err := Detector{
		Config:         config.Config{LocationPublicIPEnabled: true},
		Client:         server.Client(),
		IPInfoEndpoint: server.URL,
		CloudDetect: func(context.Context) (*api.NodeLocation, error) {
			return &api.NodeLocation{
				Provider: "aws",
				Source:   "cloud_metadata",
				Region:   "eu-central-1",
				Zone:     "eu-central-1a",
			}, nil
		},
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if ipInfoCalls != 0 {
		t.Fatalf("IPinfo calls = %d, want 0", ipInfoCalls)
	}
	if location == nil || location.Provider != "aws" {
		t.Fatalf("unexpected location: %+v", location)
	}
}

func TestDetectorUsesIPInfoWhenEnabledAndCloudUnavailable(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ip":      "203.0.113.10",
			"city":    "Istanbul",
			"region":  "Istanbul",
			"country": "TR",
			"loc":     "41.0082,28.9784",
		})
	}))
	defer server.Close()

	location, err := Detector{
		Config: config.Config{
			LocationPublicIPEnabled: true,
			IPInfoToken:             "token-123",
		},
		Client:         server.Client(),
		IPInfoEndpoint: server.URL,
		CloudDetect: func(context.Context) (*api.NodeLocation, error) {
			return nil, errors.New("no cloud metadata")
		},
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if gotToken != "token-123" {
		t.Fatalf("token query = %q, want token-123", gotToken)
	}
	if location == nil || location.Provider != "public_ip" || location.Source != "ipinfo" {
		t.Fatalf("unexpected location: %+v", location)
	}
	if location.Region != "Istanbul, TR" {
		t.Fatalf("region = %q, want Istanbul, TR", location.Region)
	}
	if location.Latitude == nil || *location.Latitude != 41.0082 {
		t.Fatalf("latitude = %v, want 41.0082", location.Latitude)
	}
	if location.Longitude == nil || *location.Longitude != 28.9784 {
		t.Fatalf("longitude = %v, want 28.9784", location.Longitude)
	}
}

func TestDetectorAllowsIPInfoWithoutToken(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"city": "Istanbul",
			"loc":  "41.0082,28.9784",
		})
	}))
	defer server.Close()

	location, err := Detector{
		Config:         config.Config{LocationPublicIPEnabled: true},
		Client:         server.Client(),
		IPInfoEndpoint: server.URL,
		CloudDetect: func(context.Context) (*api.NodeLocation, error) {
			return nil, errors.New("no cloud metadata")
		},
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if gotToken != "" {
		t.Fatalf("token query = %q, want empty", gotToken)
	}
	if location == nil || location.Provider != "public_ip" {
		t.Fatalf("unexpected location: %+v", location)
	}
}

func TestDetectorReturnsNilWhenIPInfoDisabledAndCloudUnavailable(t *testing.T) {
	location, err := Detector{
		Config: config.Config{},
		CloudDetect: func(context.Context) (*api.NodeLocation, error) {
			return nil, nil
		},
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if location != nil {
		t.Fatalf("location = %+v, want nil", location)
	}
}

func TestDetectorHandlesIPInfoFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		payload    map[string]string
	}{
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			payload:    map[string]string{"error": "rate limited"},
		},
		{
			name:       "missing loc",
			statusCode: http.StatusOK,
			payload:    map[string]string{"city": "Istanbul"},
		},
		{
			name:       "out of range loc",
			statusCode: http.StatusOK,
			payload:    map[string]string{"loc": "91,28.9784"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.payload)
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			location, err := Detector{
				Config:         config.Config{LocationPublicIPEnabled: true},
				Client:         server.Client(),
				IPInfoEndpoint: server.URL,
				CloudDetect: func(context.Context) (*api.NodeLocation, error) {
					return nil, errors.New("no cloud metadata")
				},
			}.Detect(ctx)
			if err == nil {
				t.Fatal("expected Detect() error")
			}
			if location != nil {
				t.Fatalf("location = %+v, want nil", location)
			}
		})
	}
}
