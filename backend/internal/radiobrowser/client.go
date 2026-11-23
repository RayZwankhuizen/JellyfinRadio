package radiobrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	discoveryHost = "all.api.radio-browser.info"
	pageSize      = 1000
)

type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
	LookupAddr(context.Context, string) ([]string, error)
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
}

type Client struct {
	HTTPClient *http.Client
	Resolver   Resolver
	// BaseURLs is only a test seam. Production URLs are discovered through DNS.
	BaseURLs []string
}

type Country struct {
	Name         string `json:"name"`
	Code         string `json:"code"`
	StationCount int    `json:"station_count"`
}

func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 20 * time.Second},
		Resolver:   net.DefaultResolver,
	}
}

func (c *Client) servers(ctx context.Context) []string {
	if len(c.BaseURLs) > 0 {
		return c.BaseURLs
	}
	resolver := c.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	seen := map[string]bool{}
	var bases []string
	addHost := func(name string) {
		host := strings.TrimSuffix(strings.ToLower(name), ".")
		if !strings.HasSuffix(host, ".api.radio-browser.info") || seen[host] {
			return
		}
		seen[host] = true
		bases = append(bases, "https://"+host)
	}
	_, records, _ := resolver.LookupSRV(ctx, "api", "tcp", "radio-browser.info")
	for _, record := range records {
		addHost(record.Target)
	}
	if len(bases) > 0 {
		return bases
	}
	ips, err := resolver.LookupIP(ctx, "ip", discoveryHost)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		names, err := resolver.LookupAddr(ctx, ip.String())
		if err != nil {
			continue
		}
		for _, name := range names {
			addHost(name)
		}
	}
	return bases
}

func (c *Client) Stations(ctx context.Context, countryCode, name, language, genre string) ([]Station, error) {
	bases := c.servers(ctx)
	if len(bases) == 0 {
		return nil, fmt.Errorf("discover Radio Browser servers: no usable DNS results")
	}
	var lastErr error
	for _, base := range bases {
		stations, err := c.stationsAt(ctx, base, countryCode, name, language, genre)
		if err == nil {
			return stations, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) Countries(ctx context.Context) ([]Country, error) {
	bases := c.servers(ctx)
	if len(bases) == 0 {
		return nil, fmt.Errorf("discover Radio Browser servers: no usable DNS results")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	var lastErr error
	for _, base := range bases {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/json/countries?order=name&reverse=false", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "JellyfinSmartRadio/dev")
		req.Header.Set("Accept", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("Radio Browser returned HTTP %s", resp.Status)
			continue
		}
		var result []struct {
			Name  string          `json:"name"`
			Code  string          `json:"iso_3166_1"`
			Count json.RawMessage `json:"stationcount"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("decode Radio Browser countries: %w", err)
			continue
		}
		countries := make([]Country, 0, len(result))
		for _, item := range result {
			count, _ := strconv.Atoi(strings.Trim(string(item.Count), `"`))
			countries = append(countries, Country{Name: item.Name, Code: item.Code, StationCount: count})
		}
		return countries, nil
	}
	return nil, lastErr
}

func (c *Client) stationsAt(ctx context.Context, base, countryCode, name, language, genre string) ([]Station, error) {
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	var all []Station
	for offset := 0; ; offset += pageSize {
		query := url.Values{
			"limit":      {fmt.Sprint(pageSize)},
			"offset":     {fmt.Sprint(offset)},
			"order":      {"name"},
			"reverse":    {"false"},
			"hidebroken": {"false"},
		}
		if countryCode != "" {
			query.Set("countrycode", strings.ToUpper(countryCode))
		}
		if name != "" {
			query.Set("name", name)
		}
		if language != "" {
			query.Set("language", language)
		}
		if genre != "" {
			query.Set("tag", genre)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/json/stations/search?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "JellyfinSmartRadio/dev")
		req.Header.Set("Accept", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Radio Browser request: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("Radio Browser returned HTTP %s", resp.Status)
		}
		var page []Station
		err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode Radio Browser response: %w", err)
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
}
