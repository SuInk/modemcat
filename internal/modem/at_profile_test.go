package modem

import "testing"

func TestDetectATProfileAir780FirmwareFlavors(t *testing.T) {
	tests := []struct {
		response string
		flavor   string
		model    string
	}{
		{"AirM2M_780E_V1183_LTE_AT", "AT", "Air780E"},
		{"Manufacturer: AirM2M\r\nModel: Air780E\r\nAirM2M_780E_V1183_LTE_LSAT", "LSAT", "Air780E"},
		{"OPENLUAT\r\nAirM2M_780E_V1165_LTE_AUAT", "AUAT", "Air780E"},
		{"EigenComm\r\nAir780EP\r\nAirM2M_780EP_V1011_LTE_AT", "AT", "Air780EP"},
	}
	for _, tt := range tests {
		profile := DetectATProfile(tt.response)
		if profile.Family != ATFamilyAirM2M || profile.Flavor != tt.flavor || profile.Model != tt.model {
			t.Errorf("DetectATProfile(%q) = %+v", tt.response, profile)
		}
	}
}

func TestDetectATProfileQuectel(t *testing.T) {
	profile := DetectATProfile("Quectel\r\nEC25\r\nRevision: EC25EUXGAR08A09M1G")
	if profile.Family != ATFamilyQuectel {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestATProfileSupportsUSBNetwork(t *testing.T) {
	tests := []struct {
		profile ATProfile
		want    bool
	}{
		{ATProfile{Family: ATFamilyAirM2M, Flavor: "AT"}, true},
		{ATProfile{Family: ATFamilyAirM2M, Flavor: "LSAT"}, false},
		{ATProfile{Family: ATFamilyAirM2M, Flavor: "AUAT"}, false},
		{ATProfile{Family: ATFamilyAirM2M}, true},
		{ATProfile{Family: ATFamilyQuectel}, true},
	}
	for _, tt := range tests {
		if got := tt.profile.SupportsUSBNetwork(); got != tt.want {
			t.Errorf("%+v SupportsUSBNetwork() = %v, want %v", tt.profile, got, tt.want)
		}
	}
}

func TestParseAnyICCIDAcrossFirmwareCommands(t *testing.T) {
	for _, response := range []string{
		`+QCCID: 8986001234567890123F`,
		`+CCID: "8986001234567890123F"`,
		`+ICCID: 8986001234567890123`,
	} {
		if got := parseAnyICCID(response); got != "8986001234567890123" {
			t.Errorf("parseAnyICCID(%q) = %q", response, got)
		}
	}
}

func TestParseSETUSBMode(t *testing.T) {
	mode, ok := parseSETUSBMode("mode: 2\r\nvid: 0x19d1\r\npid: 0x1\r\nOK")
	if !ok || mode != 2 {
		t.Fatalf("parseSETUSBMode() = %d, %v", mode, ok)
	}
}
