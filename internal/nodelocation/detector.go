package nodelocation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/cloudmetadata"
	"github.com/noderax/noderax-agent/internal/config"
)

const (
	defaultIPInfoEndpoint = "https://ipinfo.io/json"
	defaultTimeout        = 2 * time.Second
)

type Detector struct {
	Config         config.Config
	Client         *http.Client
	Timeout        time.Duration
	IPInfoEndpoint string
	CloudDetect    func(context.Context) (*api.NodeLocation, error)
}

type ipInfoPayload struct {
	IP      string `json:"ip"`
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
	Loc     string `json:"loc"`
}

func Detect(ctx context.Context, cfg config.Config) (*api.NodeLocation, error) {
	return Detector{Config: cfg}.Detect(ctx)
}

func (d Detector) Detect(ctx context.Context) (*api.NodeLocation, error) {
	if location := manualLocation(d.Config); location != nil {
		return location, nil
	}

	cloudDetect := d.CloudDetect
	if cloudDetect == nil {
		cloudDetect = cloudmetadata.Detect
	}

	var lastErr error
	if location, err := cloudDetect(ctx); err == nil && location != nil {
		return location, nil
	} else if err != nil {
		lastErr = err
	}

	if !d.Config.LocationPublicIPEnabled {
		return nil, lastErr
	}

	location, err := d.detectIPInfo(ctx)
	if err != nil {
		return nil, err
	}
	return location, nil
}

func manualLocation(cfg config.Config) *api.NodeLocation {
	region := normalizeLabel(cfg.LocationManualRegion)
	if region == "" || cfg.LocationManualLatitude == nil || cfg.LocationManualLongitude == nil {
		return nil
	}

	latitude := *cfg.LocationManualLatitude
	longitude := *cfg.LocationManualLongitude
	if !validLatitude(latitude) || !validLongitude(longitude) {
		return nil
	}

	return &api.NodeLocation{
		Provider:  "manual",
		Source:    "manual",
		Region:    region,
		Zone:      normalizeLabel(cfg.LocationManualZone),
		Latitude:  cloneFloat64(latitude),
		Longitude: cloneFloat64(longitude),
	}
}

func (d Detector) detectIPInfo(ctx context.Context) (*api.NodeLocation, error) {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: d.timeout()}
	}

	endpoint, err := buildIPInfoURL(d.endpoint(), d.Config.IPInfoToken)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ipinfo status %d", resp.StatusCode)
	}

	var payload ipInfoPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return nil, err
	}

	latitude, longitude, err := parseIPInfoLoc(payload.Loc)
	if err != nil {
		return nil, err
	}

	return &api.NodeLocation{
		Provider:  "public_ip",
		Source:    "ipinfo",
		Region:    ipInfoRegion(payload),
		Latitude:  cloneFloat64(latitude),
		Longitude: cloneFloat64(longitude),
	}, nil
}

func (d Detector) endpoint() string {
	if strings.TrimSpace(d.IPInfoEndpoint) != "" {
		return strings.TrimSpace(d.IPInfoEndpoint)
	}
	return defaultIPInfoEndpoint
}

func (d Detector) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultTimeout
}

func buildIPInfoURL(endpoint, token string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("ipinfo endpoint must be an absolute URL")
	}

	if token = strings.TrimSpace(token); token != "" {
		query := parsed.Query()
		if query.Get("token") == "" {
			query.Set("token", token)
			parsed.RawQuery = query.Encode()
		}
	}

	return parsed.String(), nil
}

func parseIPInfoLoc(value string) (float64, float64, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("ipinfo loc was empty or malformed")
	}

	latitude, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse ipinfo latitude: %w", err)
	}
	longitude, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse ipinfo longitude: %w", err)
	}
	if !validLatitude(latitude) || !validLongitude(longitude) {
		return 0, 0, fmt.Errorf("ipinfo coordinates were out of range")
	}

	return latitude, longitude, nil
}

func ipInfoRegion(payload ipInfoPayload) string {
	parts := make([]string, 0, 3)
	for _, value := range []string{payload.City, payload.Region, payload.Country} {
		value = normalizeLabel(value)
		if value == "" || containsEqualFold(parts, value) {
			continue
		}
		parts = append(parts, value)
	}
	if len(parts) > 0 {
		return normalizeLabel(strings.Join(parts, ", "))
	}
	if ip := normalizeLabel(payload.IP); ip != "" {
		return ip
	}
	return "public-ip"
}

func normalizeLabel(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	runes := []rune(normalized)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return normalized
}

func containsEqualFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func validLatitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -90 && value <= 90
}

func validLongitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -180 && value <= 180
}

func cloneFloat64(value float64) *float64 {
	cloned := value
	return &cloned
}
