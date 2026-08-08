package modem

import (
	"strconv"
	"strings"
)

type ATFamily string

const (
	ATFamilyUnknown ATFamily = "unknown"
	ATFamilyQuectel ATFamily = "quectel"
	ATFamilyAirM2M  ATFamily = "airm2m"
)

type ATProfile struct {
	Family ATFamily `json:"family"`
	Flavor string   `json:"flavor,omitempty"`
	Model  string   `json:"model,omitempty"`
}

func (p ATProfile) SupportsUSBNetwork() bool {
	if p.Family != ATFamilyAirM2M {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(p.Flavor)) {
	case "LSAT", "AUAT":
		return false
	default:
		return true
	}
}

// DetectATProfile classifies firmware from standard identity responses. Air780
// releases use several USB layouts, but their ATI/CGMM/CGMR strings retain the
// AirM2M_780 marker and the AT/LSAT/AUAT flavor suffix.
func DetectATProfile(response string) ATProfile {
	upper := strings.ToUpper(response)
	profile := ATProfile{Family: ATFamilyUnknown}
	switch {
	case strings.Contains(upper, "AIRM2M"), strings.Contains(upper, "AIR780"),
		strings.Contains(upper, "OPENLUAT"), strings.Contains(upper, "EIGENCOMM"):
		profile.Family = ATFamilyAirM2M
	case strings.Contains(upper, "QUECTEL"), strings.Contains(upper, "EC25"),
		strings.Contains(upper, "EG25"), strings.Contains(upper, "DJI CELLULAR"):
		profile.Family = ATFamilyQuectel
	}

	for _, line := range splitLines(response) {
		lineUpper := strings.ToUpper(line)
		if profile.Model == "" {
			if at := strings.Index(lineUpper, "AIR780"); at >= 0 {
				end := at
				for end < len(line) {
					c := line[end]
					if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
						break
					}
					end++
				}
				profile.Model = line[at:end]
			} else if marker := strings.Index(lineUpper, "AIRM2M_780"); marker >= 0 {
				start := marker + len("AIRM2M_")
				end := start
				for end < len(line) {
					c := line[end]
					if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
						break
					}
					end++
				}
				profile.Model = "Air" + line[start:end]
			}
		}
		switch {
		case strings.Contains(lineUpper, "_LTE_LSAT"):
			profile.Flavor = "LSAT"
		case strings.Contains(lineUpper, "_LTE_AUAT"):
			profile.Flavor = "AUAT"
		case strings.Contains(lineUpper, "_LTE_AT") && profile.Flavor == "":
			profile.Flavor = "AT"
		}
	}
	return profile
}

func parseAnyICCID(resp string) string {
	for _, prefix := range []string{"+QCCID:", "+CCID:", "+ICCID:"} {
		if line, ok := findLineWithPrefix(resp, prefix); ok {
			value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "\"")
			value = strings.TrimRight(value, "Ff")
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func parseSETUSBMode(resp string) (int, bool) {
	for _, line := range splitLines(resp) {
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "mode:") {
			continue
		}
		if mode, err := strconv.Atoi(strings.TrimSpace(line[len("mode:"):])); err == nil {
			return mode, true
		}
	}
	return -1, false
}
