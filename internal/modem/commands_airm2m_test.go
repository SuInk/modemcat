package modem

import (
	"testing"
	"time"
)

func airM2MTestManager(t *testing.T, flavor string) *Manager {
	t.Helper()
	m := newRunningTestManager(t)
	m.atProfile = ATProfile{Family: ATFamilyAirM2M, Flavor: flavor, Model: "Air780E"}
	return m
}

func nextAirM2MCommand(t *testing.T, m *Manager) commandRequest {
	t.Helper()
	select {
	case req := <-m.cmdChan:
		return req
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for AirM2M command")
		return commandRequest{}
	}
}

func TestAirM2MQuerySIMInsertedUsesCPINOnly(t *testing.T) {
	m := airM2MTestManager(t, "AT")
	done := make(chan struct {
		inserted bool
		err      error
	}, 1)
	go func() {
		inserted, err := m.QuerySIMInserted()
		done <- struct {
			inserted bool
			err      error
		}{inserted, err}
	}()

	req := nextAirM2MCommand(t, m)
	if req.cmd != "AT+CPIN?" {
		t.Fatalf("command = %q, want AT+CPIN?", req.cmd)
	}
	req.respChan <- "+CPIN: READY\r\nOK"
	result := <-done
	if result.err != nil || !result.inserted {
		t.Fatalf("QuerySIMInserted() = %v, %v", result.inserted, result.err)
	}
}

func TestAirM2MQueryFirmwarePrefersVER(t *testing.T) {
	m := airM2MTestManager(t, "LSAT")
	done := make(chan struct {
		firmware string
		err      error
	}, 1)
	go func() {
		firmware, err := m.QueryFirmware()
		done <- struct {
			firmware string
			err      error
		}{firmware, err}
	}()

	req := nextAirM2MCommand(t, m)
	if req.cmd != "AT+VER" {
		t.Fatalf("command = %q, want AT+VER", req.cmd)
	}
	req.respChan <- "AirM2M_780E_V1183_LTE_LSAT\r\nOK"
	result := <-done
	if result.err != nil || result.firmware != "AirM2M_780E_V1183_LTE_LSAT" {
		t.Fatalf("QueryFirmware() = %q, %v", result.firmware, result.err)
	}
}

func TestAirM2MQueryServingCellUsesCESQ(t *testing.T) {
	m := airM2MTestManager(t, "AT")
	done := make(chan struct {
		info ServingCellLTEInfo
		err  error
	}, 1)
	go func() {
		info, err := m.QueryServingCellLTEInfo()
		done <- struct {
			info ServingCellLTEInfo
			err  error
		}{info, err}
	}()

	req := nextAirM2MCommand(t, m)
	if req.cmd != "AT+CESQ" {
		t.Fatalf("command = %q, want AT+CESQ", req.cmd)
	}
	req.respChan <- "+CESQ: 99,99,255,255,20,53\r\nOK"
	result := <-done
	if result.err != nil || result.info.RSRP != -88 || result.info.RSRQ != -10 {
		t.Fatalf("QueryServingCellLTEInfo() = %+v, %v", result.info, result.err)
	}
}

func TestAirM2MFirmwareWithoutUSBNetworkSkipsVendorCommands(t *testing.T) {
	m := airM2MTestManager(t, "AUAT")
	if mode, err := m.QueryUSBNetMode(); err != nil || mode != -1 {
		t.Fatalf("QueryUSBNetMode() = %d, %v", mode, err)
	}
	if ims, err := m.QueryIMSStatus(); err != nil || ims != 0 {
		t.Fatalf("QueryIMSStatus() = %d, %v", ims, err)
	}
	select {
	case req := <-m.cmdChan:
		t.Fatalf("unexpected vendor command %q", req.cmd)
	case <-time.After(20 * time.Millisecond):
	}
}
