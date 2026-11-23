package radiobrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type srvOnlyResolver struct{ Resolver }

func (srvOnlyResolver) LookupSRV(_ context.Context, service, proto, domain string) (string, []*net.SRV, error) {
	if service != "api" || proto != "tcp" || domain != "radio-browser.info" {
		return "", nil, fmt.Errorf("unexpected SRV lookup: %s %s %s", service, proto, domain)
	}
	return "", []*net.SRV{{Target: "DE1.api.radio-browser.info."}, {Target: "de1.api.radio-browser.info."}}, nil
}

func TestServersWithoutReverseDNS(t *testing.T) {
	client := &Client{Resolver: srvOnlyResolver{}}
	got := client.servers(context.Background())
	if len(got) != 1 || got[0] != "https://de1.api.radio-browser.info" {
		t.Fatalf("unexpected servers: %v", got)
	}
}

func TestStationsUsesFiltersAndPaging(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/json/stations/search" || r.Header.Get("User-Agent") == "" {
			t.Fatalf("unexpected request: %s %v", r.URL.Path, r.Header)
		}
		q := r.URL.Query()
		if q.Get("countrycode") != "NL" || q.Get("name") != "Jazz" || q.Get("language") != "Dutch" || q.Get("tag") != "jazz" {
			t.Fatalf("filters not sent: %v", q)
		}
		if requests == 1 {
			stations := make([]Station, pageSize)
			for i := range stations {
				stations[i] = Station{UUID: fmt.Sprintf("station-%d", i), Name: "Jazz"}
			}
			_ = json.NewEncoder(w).Encode(stations)
			return
		}
		if q.Get("offset") != "1000" {
			t.Errorf("offset = %s", q.Get("offset"))
		}
		fmt.Fprint(w, `[{"stationuuid":"last","name":"Jazz"}]`)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	client := &Client{HTTPClient: srv.Client(), BaseURLs: []string{base.String()}}
	got, err := client.Stations(context.Background(), "nl", "Jazz", "Dutch", "jazz")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1001 || requests != 2 || got[1000].UUID != "last" {
		t.Fatalf("got %d stations over %d requests", len(got), requests)
	}
}

func TestStationsRejectsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer srv.Close()
	client := &Client{HTTPClient: srv.Client(), BaseURLs: []string{srv.URL}}
	if _, err := client.Stations(context.Background(), "BE", "", "", ""); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestCountriesUsesDiscoveredServerAndMapsCountryCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/countries" || r.Header.Get("User-Agent") == "" {
			t.Fatalf("unexpected request %s", r.URL)
		}
		fmt.Fprint(w, `[{"name":"Netherlands","iso_3166_1":"NL","stationcount":"123"}]`)
	}))
	defer srv.Close()
	client := &Client{HTTPClient: srv.Client(), BaseURLs: []string{srv.URL}}
	countries, err := client.Countries(context.Background())
	if err != nil || len(countries) != 1 || countries[0].Code != "NL" || countries[0].StationCount != 123 {
		t.Fatalf("countries=%+v err=%v", countries, err)
	}
}

func TestLastCheckOKAcceptsRadioBrowserNumericValues(t *testing.T) {
	var stations []Station
	if err := json.Unmarshal([]byte(`[{"stationuuid":"online","lastcheckok":1},{"stationuuid":"offline","lastcheckok":0}]`), &stations); err != nil {
		t.Fatal(err)
	}
	if len(stations) != 2 || !stations[0].LastCheckOK || stations[1].LastCheckOK {
		t.Fatalf("unexpected availability values: %+v", stations)
	}
}
