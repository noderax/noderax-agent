package cloudmetadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDetectorDetectsAWSWithIMDSv2(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			if r.Method != http.MethodPut {
				t.Errorf("token method = %s, want PUT", r.Method)
			}
			if got := r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"); got != "60" {
				t.Errorf("token ttl header = %q, want 60", got)
			}
			_, _ = w.Write([]byte("metadata-token"))
		case "/latest/dynamic/instance-identity/document":
			if got := r.Header.Get("X-aws-ec2-metadata-token"); got != "metadata-token" {
				t.Errorf("metadata token header = %q, want metadata-token", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"region":           "eu-central-1",
				"availabilityZone": "eu-central-1a",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	location, err := Detector{
		Client:        server.Client(),
		Timeout:       time.Second,
		AWSEndpoint:   server.URL,
		GCPEndpoint:   "",
		AzureEndpoint: "",
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if location == nil {
		t.Fatal("expected location, got nil")
	}
	if location.Provider != "aws" || location.Region != "eu-central-1" || location.Zone != "eu-central-1a" {
		t.Fatalf("unexpected location: %+v", location)
	}
}

func TestDetectorDetectsGCPAndDerivesRegion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Metadata-Flavor"); got != "Google" {
			t.Errorf("metadata flavor header = %q, want Google", got)
		}
		_, _ = w.Write([]byte("projects/123/zones/europe-west3-c"))
	}))
	defer server.Close()

	location, err := Detector{
		Client:        server.Client(),
		Timeout:       time.Second,
		AWSEndpoint:   "",
		GCPEndpoint:   server.URL,
		AzureEndpoint: "",
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if location == nil {
		t.Fatal("expected location, got nil")
	}
	if location.Provider != "gcp" || location.Region != "europe-west3" || location.Zone != "europe-west3-c" {
		t.Fatalf("unexpected location: %+v", location)
	}
}

func TestDetectorDetectsAzure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Metadata"); got != "true" {
			t.Errorf("metadata header = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"location": "westeurope",
			"zone":     "1",
		})
	}))
	defer server.Close()

	location, err := Detector{
		Client:        server.Client(),
		Timeout:       time.Second,
		AWSEndpoint:   "",
		GCPEndpoint:   "",
		AzureEndpoint: server.URL,
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if location == nil {
		t.Fatal("expected location, got nil")
	}
	if location.Provider != "azure" || location.Region != "westeurope" || location.Zone != "1" {
		t.Fatalf("unexpected location: %+v", location)
	}
}

func TestDetectorFallsBackWhenProviderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			http.Error(w, "not aws", http.StatusNotFound)
		case "/computeMetadata/v1/instance/zone":
			_, _ = w.Write([]byte("projects/123/zones/us-central1-a"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	location, err := Detector{
		Client:        server.Client(),
		Timeout:       time.Second,
		AWSEndpoint:   server.URL,
		GCPEndpoint:   server.URL,
		AzureEndpoint: "",
	}.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if location == nil {
		t.Fatal("expected fallback location, got nil")
	}
	if location.Provider != "gcp" || location.Region != "us-central1" {
		t.Fatalf("unexpected fallback location: %+v", location)
	}
}
