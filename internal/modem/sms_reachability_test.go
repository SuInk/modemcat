package modem

import "testing"

func TestParseReachability(t *testing.T) {
	cases := []struct {
		name             string
		creg, ims        string
		wantReachable    bool
		wantIMSMode      int
		wantIMSEnabled   bool
		wantVoLTECapable bool
	}{
		{
			name: "Auto 未提供 VoLTE 能力",
			creg: "+CREG: 0,3\r\nOK", ims: `+QCFG: "ims",0,0` + "\r\nOK",
			wantIMSMode: 0, wantIMSEnabled: true,
		},
		{
			name: "强制启用但无 VoLTE 能力",
			creg: "+CREG: 0,3\r\nOK", ims: `+QCFG: "ims",1,0` + "\r\nOK",
			wantIMSMode: 1, wantIMSEnabled: true,
		},
		{
			name: "Auto 加 VoLTE 能力实测可达",
			creg: "+CREG: 0,3\r\nOK", ims: `+QCFG: "ims",0,1` + "\r\nOK",
			wantReachable: true, wantIMSMode: 0, wantIMSEnabled: true, wantVoLTECapable: true,
		},
		{
			name: "强制 IMS 加 VoLTE 能力可达",
			creg: "+CREG: 0,3\r\nOK", ims: `+QCFG: "ims",1,1` + "\r\nOK",
			wantReachable: true, wantIMSMode: 1, wantIMSEnabled: true, wantVoLTECapable: true,
		},
		{
			name: "CS 漫游注册即可达",
			creg: "+CREG: 5\r\nOK", ims: `+QCFG: "ims",2,0` + "\r\nOK",
			wantReachable: true, wantIMSMode: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ParseReachability(c.creg, c.ims)
			if r.Reachable != c.wantReachable {
				t.Errorf("Reachable = %v, 期望 %v", r.Reachable, c.wantReachable)
			}
			if r.IMSMode != c.wantIMSMode {
				t.Errorf("IMSMode = %v, 期望 %v", r.IMSMode, c.wantIMSMode)
			}
			if r.IMSEnabled != c.wantIMSEnabled {
				t.Errorf("IMSEnabled = %v, 期望 %v", r.IMSEnabled, c.wantIMSEnabled)
			}
			if r.VoLTECapable != c.wantVoLTECapable {
				t.Errorf("VoLTECapable = %v, 期望 %v", r.VoLTECapable, c.wantVoLTECapable)
			}
			if !r.Reachable && r.Remedy == "" {
				t.Error("不可达时必须给出可执行的修复建议")
			}
			if r.Reachable && r.Remedy != "" {
				t.Errorf("可达时不应有修复建议，得到 %q", r.Remedy)
			}
		})
	}
}
