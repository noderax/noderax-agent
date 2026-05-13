package cloudmetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
)

const (
	sourceCloudMetadata = "cloud_metadata"
	defaultTimeout      = 2 * time.Second
)

type Detector struct {
	Client        *http.Client
	Timeout       time.Duration
	AWSEndpoint   string
	GCPEndpoint   string
	AzureEndpoint string
}

func DefaultDetector() Detector {
	return Detector{
		Client:        defaultHTTPClient(defaultTimeout),
		Timeout:       defaultTimeout,
		AWSEndpoint:   "http://169.254.169.254",
		GCPEndpoint:   "http://metadata.google.internal",
		AzureEndpoint: "http://169.254.169.254",
	}
}

func Detect(ctx context.Context) (*api.NodeLocation, error) {
	return DefaultDetector().Detect(ctx)
}

func (d Detector) Detect(ctx context.Context) (*api.NodeLocation, error) {
	if d.Client == nil {
		d.Client = defaultHTTPClient(d.timeout())
	}

	providers := []func(context.Context) (*api.NodeLocation, error){
		d.detectAWS,
		d.detectGCP,
		d.detectAzure,
	}

	var lastErr error
	for _, detect := range providers {
		location, err := detect(ctx)
		if err == nil && location != nil {
			return location, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	return nil, lastErr
}

func (d Detector) detectAWS(ctx context.Context) (*api.NodeLocation, error) {
	base := strings.TrimRight(d.AWSEndpoint, "/")
	if base == "" {
		return nil, nil
	}

	tokenCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	tokenReq, err := http.NewRequestWithContext(tokenCtx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return nil, err
	}
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")

	tokenResp, err := d.Client.Do(tokenReq)
	if err != nil {
		return d.detectAWSIdentityDocument(ctx, base, "")
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode < 200 || tokenResp.StatusCode >= 300 {
		location, docErr := d.detectAWSIdentityDocument(ctx, base, "")
		if docErr == nil && location != nil {
			return location, nil
		}
		return nil, fmt.Errorf("aws metadata token status %d", tokenResp.StatusCode)
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(tokenResp.Body, 4096))
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return nil, fmt.Errorf("aws metadata token was empty")
	}

	return d.detectAWSIdentityDocument(ctx, base, token)
}

func (d Detector) detectAWSIdentityDocument(ctx context.Context, base, token string) (*api.NodeLocation, error) {
	docCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	docReq, err := http.NewRequestWithContext(docCtx, http.MethodGet, base+"/latest/dynamic/instance-identity/document", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		docReq.Header.Set("X-aws-ec2-metadata-token", token)
	}

	var doc struct {
		Region           string `json:"region"`
		AvailabilityZone string `json:"availabilityZone"`
	}
	if err := d.getJSON(docReq, &doc); err != nil {
		return nil, err
	}

	return buildLocation("aws", doc.Region, doc.AvailabilityZone), nil
}

func (d Detector) detectGCP(ctx context.Context) (*api.NodeLocation, error) {
	base := strings.TrimRight(d.GCPEndpoint, "/")
	if base == "" {
		return nil, nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/computeMetadata/v1/instance/zone", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gcp metadata status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, err
	}
	zone := lastPathSegment(strings.TrimSpace(string(body)))
	region := deriveGCPRegion(zone)

	return buildLocation("gcp", region, zone), nil
}

func (d Detector) detectAzure(ctx context.Context) (*api.NodeLocation, error) {
	base := strings.TrimRight(d.AzureEndpoint, "/")
	if base == "" {
		return nil, nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/metadata/instance/compute?api-version=2021-02-01", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata", "true")

	var doc struct {
		Location string `json:"location"`
		Zone     string `json:"zone"`
	}
	if err := d.getJSON(req, &doc); err != nil {
		return nil, err
	}

	return buildLocation("azure", doc.Location, doc.Zone), nil
}

func (d Detector) getJSON(req *http.Request, target any) error {
	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("metadata status %d", resp.StatusCode)
	}

	return json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(target)
}

func (d Detector) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultTimeout
}

func defaultHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: timeout, Transport: transport}
}

func buildLocation(provider, region, zone string) *api.NodeLocation {
	provider = strings.TrimSpace(strings.ToLower(provider))
	region = strings.TrimSpace(strings.ToLower(region))
	zone = strings.TrimSpace(strings.ToLower(zone))
	if provider == "" || region == "" {
		return nil
	}

	return &api.NodeLocation{
		Provider: provider,
		Source:   sourceCloudMetadata,
		Region:   region,
		Zone:     zone,
	}
}

func lastPathSegment(value string) string {
	value = strings.TrimRight(value, "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func deriveGCPRegion(zone string) string {
	zone = strings.TrimSpace(strings.ToLower(zone))
	if zone == "" {
		return ""
	}
	if index := strings.LastIndex(zone, "-"); index > 0 {
		return zone[:index]
	}
	return zone
}
