package modem

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/SuInk/modemcat/pkg/logger"
)

var (
	cregStatRe  = regexp.MustCompile(`\+CREG:\s*(?:\d+,\s*)?(\d+)`)
	ceregStatRe = regexp.MustCompile(`\+CEREG:\s*(?:\d+,\s*)?(\d+)`)
	imsCfgRe    = regexp.MustCompile(`\+QCFG:\s*"ims",\s*(\d+)(?:,\s*(\d+))?`)
)

// SMSReachability 描述短信能否送达，与数据是否可用无关。
//
// LTE 没有电路交换域，短信只能走 CSFB（需 CS 域注册）或 SMS over IMS（需 IMS 注册）。
// 中国电信 CDMA 已退网且从无 GSM，其 SIM 没有 CS 域可退，IMS 是唯一通路；中国移动仍有
// GSM，CSFB 可用。模块出厂 ims=0，遇到前一类卡时数据完全正常、信号满格、短信中心配置
// 齐全，唯一症状就是收不到短信。
type SMSReachability struct {
	CSRegistered bool
	IMSMode      int
	IMSEnabled   bool
	VoLTECapable bool
	Reachable    bool
	Remedy       string
}

// ParseReachability 从 AT+CREG? 与 AT+QCFG="ims" 的响应推断短信通路。
func ParseReachability(cregResp, imsResp string) SMSReachability {
	r := SMSReachability{IMSMode: -1}

	if mm := cregStatRe.FindStringSubmatch(cregResp); mm != nil {
		switch mm[1] {
		case "1", "5": // 已注册（本地 / 漫游）
			r.CSRegistered = true
		}
	}

	if mm := imsCfgRe.FindStringSubmatch(imsResp); mm != nil {
		mode, _ := strconv.Atoi(mm[1])
		r.IMSMode = mode
		r.IMSEnabled = mode != 2
		if mm[2] != "" {
			capability, _ := strconv.Atoi(mm[2])
			r.VoLTECapable = capability == 1
		}
	}

	// The second QCFG field is VoLTE_cap, not an IMS registration flag. On the
	// tested QDC507, Auto mode (0) with capability 1 receives SMS over IMS.
	r.Reachable = r.CSRegistered || (r.IMSEnabled && r.VoLTECapable)
	switch {
	case r.Reachable:
	case r.IMSMode == 2:
		r.Remedy = `IMS 被强制禁用，当前网络又没有 CS 短信通路。可在 AT 调试页执行 AT+QCFG="ims",0 恢复自动配置后重启模块`
	case r.IMSMode == 0:
		r.Remedy = `IMS 由 MBN 自动配置，当前网络未提供 VoLTE 能力。可在 AT 调试页执行 AT+QCFG="ims",1 强制启用后重启模块；也可能是套餐未开通短信或当地不支持 VoLTE`
	case r.IMSMode == 1:
		r.Remedy = "IMS 已强制启用但当前未提供 VoLTE 能力，请检查 SIM 套餐、MBN 与运营商支持"
	default:
		r.Remedy = "无法读取 IMS 配置，短信通路状态未知"
	}
	return r
}

func (m *Manager) querySMSReachability() SMSReachability {
	cregResp, _ := m.ExecuteATSilent("AT+CREG?", 2*time.Second)
	if m.ATProfile().Family == ATFamilyAirM2M {
		ceregResp, _ := m.ExecuteATSilent("AT+CEREG?", 2*time.Second)
		registered := registrationResponseReady(cregStatRe, cregResp) || registrationResponseReady(ceregStatRe, ceregResp)
		result := SMSReachability{IMSMode: -1, CSRegistered: registered, Reachable: registered}
		if !registered {
			result.Remedy = "Air780 尚未注册移动网络，请检查 SIM、天线、信号和运营商注册状态"
		}
		return result
	}
	imsResp, _ := m.ExecuteATSilent(`AT+QCFG="ims"`, 2*time.Second)
	return ParseReachability(cregResp, imsResp)
}

func registrationResponseReady(pattern *regexp.Regexp, response string) bool {
	match := pattern.FindStringSubmatch(response)
	return len(match) == 2 && (match[1] == "1" || match[1] == "5")
}

// SMSReachabilityState 供上层展示：是否可达、以及不可达时的原因说明。
func (m *Manager) SMSReachabilityState() (reachable bool, detail string) {
	m.infoMu.RLock()
	defer m.infoMu.RUnlock()
	return m.smsReachable, m.smsUnreachable
}

// checkSMSReachability 在初始化后确认短信通路，只观测不改写。
//
// 刻意不自动写 AT+QCFG="ims",1：改写 IMS 配置会影响 VoLTE/VoWiFi 行为，还需要复位模块
// 才生效，属于用户该自己决定的事。程序的职责是把「短信为什么收不到」讲清楚——Remedy
// 里给出可直接照做的指令——而不是替用户改设备配置。
func (m *Manager) checkSMSReachability() {
	r := m.querySMSReachability()

	m.infoMu.Lock()
	m.smsReachable = r.Reachable
	m.smsUnreachable = r.Remedy
	m.infoMu.Unlock()

	if !r.Reachable {
		logger.Warn(fmt.Sprintf("[%s] 短信当前不可达", m.cfg.ID),
			"cs_registered", r.CSRegistered,
			"ims_mode", r.IMSMode,
			"ims_enabled", r.IMSEnabled,
			"volte_capable", r.VoLTECapable,
			"remedy", r.Remedy)
	}
}
