package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortScore(t *testing.T) {
	tests := []struct {
		name string
		port string
		want int
	}{
		{name: "named Quectel port", port: "/dev/cu.Quectel-AT", want: 100},
		{name: "usb modem", port: "/dev/cu.usbmodem2101", want: 80},
		{name: "usb serial", port: "/dev/cu.usbserial-1420", want: 60},
		{name: "bluetooth", port: "/dev/cu.Bluetooth-Incoming-Port", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := portScore(tt.port); got != tt.want {
				t.Fatalf("portScore(%q) = %d, want %d", tt.port, got, tt.want)
			}
		})
	}
}

func TestParseUSBNetMode(t *testing.T) {
	for _, tt := range []struct {
		response string
		want     string
	}{
		{response: "AT+QCFG=\"usbnet\"\r\n+QCFG: \"usbnet\",0\r\nOK", want: "0"},
		{response: "+QCFG: \"usbnet\",1\r\nOK", want: "1"},
		{response: "ERROR", want: ""},
	} {
		if got := parseUSBNetMode(tt.response); got != tt.want {
			t.Fatalf("parseUSBNetMode(%q) = %q, want %q", tt.response, got, tt.want)
		}
	}
}

func TestParseMacHardwarePorts(t *testing.T) {
	out := `An asterisk (*) denotes that a network service is disabled.

Hardware Port: Wi-Fi
Device: en1
Ethernet Address: aa:bb:cc:dd:ee:ff

Hardware Port: Baiwang
Device: en8
Ethernet Address: 00:11:22:33:44:55
`
	want := []macHardwarePort{{Name: "Wi-Fi", Device: "en1"}, {Name: "Baiwang", Device: "en8"}}
	got := parseMacHardwarePorts(out)
	if len(got) != len(want) {
		t.Fatalf("parseMacHardwarePorts() returned %d ports, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("port %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestSelectUSBTrafficInterfaceRequiresDJIHardwarePort(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en1", Status: "active", Kind: "ethernet", IPv4: "192.168.3.49"},
		{Name: "en8", Status: "active", Kind: "ethernet", IPv4: "192.168.225.2"},
	}
	tests := []struct {
		name  string
		route macDefaultRoute
		ports []macHardwarePort
		want  string
	}{
		{
			name:  "ordinary en1 is not treated as cellular",
			route: macDefaultRoute{Interface: "en1"},
			ports: []macHardwarePort{{Name: "Wi-Fi", Device: "en1"}},
		},
		{
			name:  "Baiwang hardware port is selected",
			route: macDefaultRoute{Interface: "en1"},
			ports: []macHardwarePort{{Name: "Wi-Fi", Device: "en1"}, {Name: "Baiwang", Device: "en8"}},
			want:  "en8",
		},
		{
			name:  "DJI name is accepted case insensitively",
			route: macDefaultRoute{Interface: "en8"},
			ports: []macHardwarePort{{Name: "DJI 4G Modem", Device: "en8"}},
			want:  "en8",
		},
		{
			name:  "unrelated USB Ethernet is rejected",
			route: macDefaultRoute{Interface: "en8"},
			ports: []macHardwarePort{{Name: "USB 10/100/1000 LAN", Device: "en8"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectUSBTrafficInterface(interfaces, tt.route, tt.ports); got != tt.want {
				t.Fatalf("selectUSBTrafficInterface() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseUSBATOperator(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "known numeric PLMN",
			response: "AT+COPS?\r\n+COPS: 0,2,\"46015\",7\r\nOK",
			want:     "中国广电",
		},
		{
			name:     "long operator name",
			response: "+COPS: 0,0,\"CHN-UNICOM\",7\r\nOK",
			want:     "CHN-UNICOM",
		},
		{
			name:     "missing operator",
			response: "+COPS: 0\r\nOK",
			want:     "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseUSBATOperator(tt.response); got != tt.want {
				t.Fatalf("parseUSBATOperator(%q) = %q, want %q", tt.response, got, tt.want)
			}
		})
	}
}

func TestInitUSBATESIMManagerAfterDelayedUSBOpen(t *testing.T) {
	instance := &app{}

	manager, switchAllowed := instance.currentESIMManager()
	if manager != nil || switchAllowed {
		t.Fatalf("initial eSIM state = (%v, %v), want unavailable", manager, switchAllowed)
	}

	instance.initUSBATESIMManager()
	manager, switchAllowed = instance.currentESIMManager()
	if manager == nil {
		t.Fatal("USB AT recovery did not initialize the eSIM manager")
	}
	if !switchAllowed {
		t.Fatal("USB AT eSIM manager should allow profile switching")
	}

	instance.initUSBATESIMManager()
	managerAgain, _ := instance.currentESIMManager()
	if managerAgain != manager {
		t.Fatal("repeated USB AT recovery replaced the existing eSIM manager")
	}
}

func TestSameOriginMutation(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		fetchSite string
		public    string
		want      bool
	}{
		{name: "CLI without browser headers", host: "127.0.0.1:7575", want: true},
		{name: "same origin", host: "127.0.0.1:7575", origin: "http://127.0.0.1:7575", fetchSite: "same-origin", want: true},
		{name: "cross origin", host: "127.0.0.1:7575", origin: "https://example.com", want: false},
		{name: "cross site form", host: "127.0.0.1:7575", fetchSite: "cross-site", want: false},
		{name: "DNS rebinding host", host: "evil.test:7575", origin: "http://evil.test:7575", fetchSite: "same-origin", want: false},
		{name: "Cloudflare HTTPS origin", host: "djsmsforward.example.com", origin: "https://djsmsforward.example.com", fetchSite: "same-origin", public: "https://djsmsforward.example.com", want: true},
		{name: "Cloudflare HTTP downgrade", host: "djsmsforward.example.com", origin: "http://djsmsforward.example.com", fetchSite: "same-origin", public: "https://djsmsforward.example.com", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://127.0.0.1:7575/api/test", nil)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if got := sameOriginMutation(request, tt.public); got != tt.want {
				t.Fatalf("sameOriginMutation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidatePublicOrigin(t *testing.T) {
	for _, value := range []string{"", "https://djsmsforward.example.com", "https://djsmsforward.example.com:8443"} {
		if _, err := validatePublicOrigin(value); err != nil {
			t.Errorf("validatePublicOrigin(%q) = %v", value, err)
		}
	}
	for _, value := range []string{"http://djsmsforward.example.com", "https://127.0.0.1", "https://localhost", "https://user@example.com", "https://example.com/path", "https://example.com?q=1"} {
		if _, err := validatePublicOrigin(value); err == nil {
			t.Errorf("validatePublicOrigin(%q) unexpectedly succeeded", value)
		}
	}
}

func TestLoopbackRequestHost(t *testing.T) {
	for _, tt := range []struct {
		host string
		want bool
	}{
		{host: "127.0.0.1:7575", want: true},
		{host: "localhost:7575", want: true},
		{host: "[::1]:7575", want: true},
		{host: "evil.test:7575", want: false},
		{host: "127.0.0.1.evil.test:7575", want: false},
		{host: "0.0.0.0:7575", want: false},
	} {
		if got := loopbackRequestHost(tt.host); got != tt.want {
			t.Errorf("loopbackRequestHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7575", "localhost:7575", "[::1]:7575"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) = %v", address, err)
		}
	}
	for _, address := range []string{":7575", "0.0.0.0:7575", "192.168.1.10:7575", "evil.test:7575"} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestSecurityHeadersRejectUnsafeRequests(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "")
	tests := []struct {
		name        string
		method      string
		host        string
		origin      string
		contentType string
		public      string
		want        int
	}{
		{name: "loopback GET", method: http.MethodGet, host: "127.0.0.1:7575", want: http.StatusNoContent},
		{name: "rebinding GET", method: http.MethodGet, host: "evil.test:7575", want: http.StatusForbidden},
		{name: "same-origin JSON", method: http.MethodPost, host: "127.0.0.1:7575", origin: "http://127.0.0.1:7575", contentType: "application/json", want: http.StatusNoContent},
		{name: "simple form", method: http.MethodPost, host: "127.0.0.1:7575", origin: "http://127.0.0.1:7575", contentType: "text/plain", want: http.StatusForbidden},
		{name: "allowed Cloudflare host", method: http.MethodGet, host: "djsmsforward.example.com", public: "https://djsmsforward.example.com", want: http.StatusNoContent},
		{name: "unconfigured public host", method: http.MethodGet, host: "djsmsforward.example.com", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(tt.method, "http://127.0.0.1:7575/api/test", nil)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if tt.contentType != "" {
				request.Header.Set("Content-Type", tt.contentType)
			}
			recorder := httptest.NewRecorder()
			candidate := handler
			if tt.public != "" {
				candidate = securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}), tt.public)
			}
			candidate.ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}
