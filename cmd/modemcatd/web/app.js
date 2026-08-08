const $ = (selector) => document.querySelector(selector);
let lastSMSCount = null;
let smsMessages = [];
let selectedSMSContact = "";
let smsConversationSignature = "";
let esimHealthPollTimer = null;
let esimHealthInFlight = false;
let networkTrafficTimer = null;
let networkTrafficPrevious = null;
let networkTrafficInFlight = false;
let callPollInFlight = false;
let activeCallIndex = null;
let activeCallState = "";
let activeCallRenderSignature = "";
let callHistoryRenderSignature = "";
let callAudioBridge = null;
let notificationSettingsInFlight = false;
let notificationActionInFlight = false;
let notificationEventSource = null;
let scheduledTasksInFlight = false;
let scheduledTaskRenderSignature = "";
let currentModuleFamily = "unknown";
let currentFirmwareFlavor = "";
let currentUSBNetworkSupported = true;

function setThemePreference(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("modemcat-theme", theme);
  } else {
    delete document.documentElement.dataset.theme;
    localStorage.removeItem("modemcat-theme");
  }
  document.querySelectorAll("[data-theme-option]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themeOption === theme));
  });
}

const savedTheme = localStorage.getItem("modemcat-theme");
setThemePreference(savedTheme === "light" || savedTheme === "dark" ? savedTheme : "auto");
document.querySelectorAll("[data-theme-option]").forEach((button) => {
  button.addEventListener("click", () => setThemePreference(button.dataset.themeOption));
});

const operatorNames = new Map([
  ["CHN-UNICOM", "中国联通"],
  ["CHINA UNICOM", "中国联通"],
  ["UNICOM", "中国联通"],
  ["46001", "中国联通"],
  ["46006", "中国联通"],
  ["46009", "中国联通"],
  ["CHINA MOBILE", "中国移动"],
  ["CMCC", "中国移动"],
  ["CHN-CMCC", "中国移动"],
  ["46000", "中国移动"],
  ["46002", "中国移动"],
  ["46004", "中国移动"],
  ["46007", "中国移动"],
  ["46008", "中国移动"],
  ["CHINA TELECOM", "中国电信"],
  ["CHN-CT", "中国电信"],
  ["CTCC", "中国电信"],
  ["46003", "中国电信"],
  ["46005", "中国电信"],
  ["46011", "中国电信"],
  ["CBN", "中国广电"],
  ["CHN-CBN", "中国广电"],
  ["CHINA BROADNET", "中国广电"],
  ["46015", "中国广电"],
]);

// 模块重启期间挂起所有后台轮询，见 waitForModule
let pollSuspended = false;

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (response.status === 401 || response.status === 428) {
    window.location.replace("/login");
    throw new Error(data.error || "请先登录");
  }
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function notice(message) {
  const el = $("#notice");
  el.textContent = message;
  el.classList.add("show");
  clearTimeout(notice.timer);
  notice.timer = setTimeout(() => el.classList.remove("show"), 2600);
}

let modalResolve = null;

function closeModal(result = null) {
  const modal = $("#app-modal");
  modal.hidden = true;
  document.body.classList.remove("modal-open");
  if (modalResolve) {
    const resolve = modalResolve;
    modalResolve = null;
    resolve(result);
  }
}

function showModal({ title, message = "", fields = [], confirmLabel = "确定", danger = false }) {
  if (modalResolve) closeModal(null);
  const modal = $("#app-modal");
  const messageElement = $("#modal-message");
  const fieldsElement = $("#modal-fields");
  const confirmButton = $("#modal-confirm");
  $("#modal-title").textContent = title;
  messageElement.textContent = message;
  messageElement.hidden = !message;
  fieldsElement.replaceChildren(...fields.map((field) => {
    const label = document.createElement("label");
    label.className = "modal-field";
    const caption = document.createElement("span");
    caption.textContent = field.label;
    const control = document.createElement(field.type === "textarea" ? "textarea" : "input");
    control.name = field.name;
    control.autocomplete = field.autocomplete || "off";
    if (field.type === "checkbox") {
      label.classList.add("modal-checkbox");
      control.type = "checkbox";
      control.checked = Boolean(field.checked);
      label.append(control, caption);
      return label;
    }
    if (control instanceof HTMLInputElement) control.type = field.type || "text";
    control.value = field.value ?? "";
    control.placeholder = field.placeholder || "";
    if (field.required) control.required = true;
    if (field.min !== undefined) control.min = String(field.min);
    if (field.max !== undefined) control.max = String(field.max);
    if (field.maxLength !== undefined) control.maxLength = Number(field.maxLength);
    label.append(caption, control);
    return label;
  }));
  confirmButton.textContent = confirmLabel;
  confirmButton.className = danger ? "danger modal-danger" : "";
  modal.hidden = false;
  document.body.classList.add("modal-open");
  const firstInput = fieldsElement.querySelector("input:not([type=checkbox]), textarea, input");
  setTimeout(() => (firstInput || confirmButton).focus(), 0);
  return new Promise((resolve) => { modalResolve = resolve; });
}

$("#modal-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const values = {};
  event.currentTarget.querySelectorAll(".modal-fields input, .modal-fields textarea").forEach((input) => {
    values[input.name] = input.type === "checkbox" ? input.checked : input.value.trim();
  });
  closeModal(values);
});
$("#modal-cancel").addEventListener("click", () => closeModal(null));
$("#modal-close").addEventListener("click", () => closeModal(null));
$("#app-modal").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeModal(null);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#app-modal").hidden) closeModal(null);
});

async function copySMSCode(code) {
  try {
    await navigator.clipboard.writeText(code);
    notice(`验证码 ${code} 已复制`);
  } catch (error) {
    notice("复制失败，请手动复制验证码");
  }
}

function renderHardwareDetails(status) {
  const panel = $("#hardware-details");
  const device = status.usb_device;
  if (!device && !status.discovery_error) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }

  const title = document.createElement("strong");
	  title.textContent = device ? "已检测到蜂窝模块 USB 设备" : "未检测到可用硬件";

  const detail = document.createElement("p");
  if (device) {
    const interfaceText = Array.isArray(device.interfaces)
      ? `${device.interfaces.length} 个 USB interface`
      : "interface 未知";
    detail.textContent = [
		`${device.vendor || "蜂窝模块"} ${device.product || ""}`.trim(),
      `${device.vendor_id}:${device.product_id}`,
      device.mode,
      interfaceText,
    ].filter(Boolean).join(" · ");
  } else {
    detail.textContent = status.discovery_error || "设备未枚举";
  }

  const hint = document.createElement("small");
  hint.textContent = status.discovery_error
    ? `当前限制：${status.discovery_error}`
    : "AT 串口可用后，短信和 eSIM/卡片操作会自动启用。";

  panel.hidden = false;
  panel.replaceChildren(title, detail, hint);
}

function setValue(id, text, tone = "") {
  const el = $(id);
  el.textContent = text || "--";
  el.className = tone;
}

function displayOperatorName(value) {
  const raw = String(value || "").trim();
  if (!raw) return "--";
  return operatorNames.get(raw.toUpperCase()) || raw;
}

function displayWorkMode(value, family = currentModuleFamily, usbNetworkSupported = currentUSBNetworkSupported) {
	if (value === null || value === undefined || value === "") {
	  return { label: "待读取", tone: "muted" };
	}
  if (!usbNetworkSupported) return { label: "AT 管理模式", tone: "info" };
  if (family === "airm2m") {
    switch (Number(value)) {
      case 1: return { label: "RNDIS 模式", tone: "warn" };
      case 2: return { label: "ECM 模式", tone: "info" };
      default: return { label: `USB 模式 ${value}`, tone: "muted" };
    }
  }
  switch (Number(value)) {
    case 0: return { label: "短信模式", tone: "info" };
    case 1: return { label: "上网模式", tone: "info" };
    case 2: return { label: "实验模式 2", tone: "warn" };
    case 3: return { label: "实验模式 3", tone: "warn" };
    default: return { label: "待读取", tone: "muted" };
  }
}

function updateModulePresentation(status) {
  currentModuleFamily = status.module_family || status.usb_device?.family || "unknown";
  currentFirmwareFlavor = status.firmware_flavor || "";
  currentUSBNetworkSupported = status.usb_network_supported !== false;
  const air = currentModuleFamily === "airm2m";
  const model = status.module_model || (air ? "Air780" : "DJI Cellular Gen 1");
  $("#module-name").textContent = model;
  $("#module-detail").textContent = [
    air ? "AirM2M / OpenLuat" : "Quectel / DJI",
    currentFirmwareFlavor ? `${currentFirmwareFlavor} 固件` : "",
    status.usb_device ? `USB ${status.usb_device.vendor_id}:${status.usb_device.product_id}` : "",
  ].filter(Boolean).join(" · ") || "正在识别型号与固件";

  $("#workmode-sms").hidden = air;
  $("#workmode-network").textContent = air ? "ECM 上网模式" : "上网模式";
  $("#workmode-network").disabled = !currentUSBNetworkSupported;
  $("#usbnet-mode-0").hidden = air;
  $("#usbnet-mode-1").textContent = air ? "模式 2 · ECM（Air780）" : "模式 1 · ECM（4G 网卡）";
  $("#usbnet-mode-0").disabled = !currentUSBNetworkSupported;
  $("#usbnet-mode-1").disabled = !currentUSBNetworkSupported;
  $("#usb-mode-help").textContent = air
    ? (currentUSBNetworkSupported
      ? "Air780 AT 固件使用 SETUSB：模式 1 是 RNDIS，模式 2 是 ECM，macOS 使用模式 2。写入前会读取并验证模块响应。"
      : `Air780 ${currentFirmwareFlavor || "当前"} 固件不提供 USB 网卡；短信、状态查询和 AT 管理仍可使用。`)
    : "Quectel 使用 QCFG usbnet：模式 0 是 QMI，模式 1 是 CDC-ECM。macOS 使用模式 1；切换后需要重启模块生效。";
}

function signalTone(dbm) {
  const value = Number(dbm);
  if (!Number.isFinite(value) || value === 0) return "muted";
  if (value >= -65) return "good";
  if (value >= -75) return "signal-fair";
  if (value >= -85) return "warn";
  if (value >= -95) return "orange";
  return "bad";
}

async function loadStatus() {
	try {
	  const status = await api("/api/status");
	  updateModulePresentation(status);
    setValue("#operator", displayOperatorName(status.operator), status.operator ? "info" : "muted");
    setValue("#signal", status.signal_dbm ? `${status.signal_dbm} dBm` : "--", signalTone(status.signal_dbm));
    setValue("#network-mode", status.network_mode || status.reg_status_text || "--", status.network_mode ? "info" : "muted");
    setValue(
      "#sim",
      status.sim_inserted ? "已插入" : (status.usb_device ? "待读取" : "未检测到"),
      status.sim_inserted ? "good" : (status.usb_device ? "warn" : "bad"),
    );
    const workMode = Object.prototype.hasOwnProperty.call(status, "usbnet_mode")
	  ? displayWorkMode(status.usbnet_mode, currentModuleFamily, currentUSBNetworkSupported)
	  : displayWorkMode(null, currentModuleFamily);
    setValue("#work-mode", workMode.label, workMode.tone);
	$("#workmode-sms").classList.toggle("active", currentModuleFamily !== "airm2m" && Number(status.usbnet_mode) === 0);
	$("#workmode-network").classList.toggle("active", currentModuleFamily === "airm2m"
	  ? Number(status.usbnet_mode) === 2
	  : Number(status.usbnet_mode) === 1);
    const reachability = $("#sms-reachability");
    const needsSMSAction = status.sms_reachable === false;
    reachability.hidden = !needsSMSAction;
    $("#sms-reachability-detail").textContent = status.sms_detail || "当前没有可确认的短信投递通路";
    // 修复建议里都以“重启模块”收尾，按钮跟着提示一起出现即可
    $("#sms-reachability-reboot").hidden = !needsSMSAction;
    $("#device-summary").textContent =
      status.hardware_status || [status.imei, status.firmware].filter(Boolean).join(" · ") || "模块初始化中";
    renderHardwareDetails(status);
  } catch (error) {
    $("#device-summary").textContent = error.message;
  }
}

async function loadSMS() {
  try {
    const [messages, status] = await Promise.all([
      api("/api/sms"),
      api("/api/sms/status"),
    ]);
    const pollText = status.polling
      ? `自动轮询 ${status.poll_interval_s || 8}s`
      : "自动轮询未启用";
    const cleanupText = status.auto_cleanup_me ? "ME 自动清理已开启" : "ME 自动清理已关闭";
    const storageText = status.persistent ? `磁盘已保存 ${messages.length} 条` : "磁盘保存异常";
    const errors = [status.last_poll_error, status.store_error].filter(Boolean);
    const errorText = errors.length ? ` · 最近错误：${errors.join("；")}` : "";
    $("#sms-status").textContent = `${storageText} · ${pollText} · ${cleanupText}${errorText}`;
    const incomingCount = messages.filter((message) => smsMessageDirection(message) === "incoming").length;
    if (lastSMSCount !== null && incomingCount > lastSMSCount) {
      notice(`收到 ${incomingCount - lastSMSCount} 条新短信`);
    }
    lastSMSCount = incomingCount;
    smsMessages = messages;
    renderSMSConversations();
  } catch (error) {
    $("#sms-status").textContent = `读取列表失败：${error.message}`;
    notice(error.message);
  }
}

function smsMessageDirection(message) {
  return message.direction === "outgoing" ? "outgoing" : "incoming";
}

function smsMessageContact(message) {
  const value = smsMessageDirection(message) === "outgoing" ? message.recipient : message.sender;
  const contact = String(value || "未知号码").trim() || "未知号码";
  const compact = contact.replace(/[\s()-]/g, "");
  if (!/^\+?\d+$/.test(compact)) return contact;
  return compact.startsWith("00") ? `+${compact.slice(2)}` : compact;
}

function smsConversationGroups() {
  const groups = new Map();
  smsMessages.forEach((message) => {
    const contact = smsMessageContact(message);
    if (!groups.has(contact)) groups.set(contact, []);
    groups.get(contact).push(message);
  });
  return [...groups.entries()]
    .map(([contact, messages]) => ({
      contact,
      messages: messages.sort((left, right) => new Date(left.timestamp) - new Date(right.timestamp)),
    }))
    .sort((left, right) => {
      const leftTime = new Date(left.messages[left.messages.length - 1]?.timestamp || 0);
      const rightTime = new Date(right.messages[right.messages.length - 1]?.timestamp || 0);
      return rightTime - leftTime;
    });
}

function smsPreview(message) {
  const prefix = smsMessageDirection(message) === "outgoing" ? "你：" : "";
  return `${prefix}${message.content || ""}`;
}

function selectSMSConversation(contact, focusComposer = false) {
  selectedSMSContact = contact;
  $("#phone").value = contact === "未知号码" ? "" : contact;
  $(".sms-workspace").classList.add("thread-open");
  renderSMSConversations();
  if (focusComposer) $("#message").focus();
}

function renderSMSConversations() {
  const list = $("#sms-conversations");
  const groups = smsConversationGroups();
  if (!selectedSMSContact && groups.length) selectedSMSContact = groups[0].contact;
  if (!groups.length) {
    list.className = "conversation-list empty";
    list.textContent = "暂无会话";
  } else {
    list.className = "conversation-list";
    list.replaceChildren(...groups.map((group) => {
      const latest = group.messages[group.messages.length - 1];
      const button = document.createElement("button");
      button.type = "button";
      button.className = `conversation-item${group.contact === selectedSMSContact ? " active" : ""}`;
      button.addEventListener("click", () => selectSMSConversation(group.contact));
      const avatar = document.createElement("span");
      avatar.className = "conversation-avatar";
      avatar.textContent = group.contact.slice(0, 1).toUpperCase();
      const body = document.createElement("span");
      body.className = "conversation-summary";
      const heading = document.createElement("span");
      heading.className = "conversation-item-heading";
      const contact = document.createElement("strong");
      contact.textContent = group.contact;
      const time = document.createElement("time");
      time.textContent = new Date(latest.timestamp).toLocaleDateString([], { month: "numeric", day: "numeric" });
      heading.append(contact, time);
      const preview = document.createElement("small");
      preview.textContent = smsPreview(latest);
      body.append(heading, preview);
      button.append(avatar, body);
      return button;
    }));
  }

  const empty = $("#conversation-empty");
  const view = $("#conversation-view");
  if (!selectedSMSContact) {
    empty.hidden = false;
    view.hidden = true;
    return;
  }
  empty.hidden = true;
  view.hidden = false;
  const group = groups.find((item) => item.contact === selectedSMSContact);
  const messages = group?.messages || [];
  $("#conversation-contact").textContent = selectedSMSContact;
  $("#conversation-count").textContent = messages.length ? `${messages.length} 条消息` : "新对话";
  renderSMSMessageStream(messages);
}

function renderSMSMessageStream(messages) {
  const stream = $("#sms-messages");
  const signature = `${selectedSMSContact}\n${messages.map((message) => [message.direction, message.sender, message.recipient, message.content, message.timestamp].join("\u0000")).join("\n")}`;
  if (signature === smsConversationSignature) return;
  smsConversationSignature = signature;
  const wasNearBottom = stream.scrollHeight - stream.scrollTop - stream.clientHeight < 80;
  stream.replaceChildren(...messages.map((message) => {
    const bubble = document.createElement("article");
    bubble.className = `message-row ${smsMessageDirection(message)}`;
    const content = document.createElement("div");
    content.className = "message-bubble";
    const text = document.createElement("p");
    text.textContent = message.content;
    const metadata = document.createElement("div");
    metadata.className = "message-metadata";
    if (message.code && smsMessageDirection(message) === "incoming") {
      const copy = document.createElement("button");
      copy.className = "message-code";
      copy.type = "button";
      copy.textContent = `验证码 ${message.code}`;
      copy.addEventListener("click", () => copySMSCode(message.code));
      metadata.append(copy);
    }
    const time = document.createElement("time");
    time.textContent = new Date(message.timestamp).toLocaleString();
    metadata.append(time);
    content.append(text, metadata);
    bubble.append(content);
    return bubble;
  }));
  if (wasNearBottom || messages.length) stream.scrollTop = stream.scrollHeight;
}

function formatScheduledTime(value) {
  const timestamp = Number(value || 0);
  if (!timestamp) return "--";
  const date = new Date(timestamp);
  return Number.isNaN(date.getTime()) ? "--" : date.toLocaleString();
}

function scheduledTaskState(task) {
  if (task.running || task.last_run_status === "running") return { label: "执行中", tone: "running" };
  if (!task.enabled) return { label: "已暂停", tone: "paused" };
  if (task.last_run_status === "failed") return { label: "上次失败", tone: "failed" };
  if (task.last_run_status === "success") return { label: "运行中 · 上次成功", tone: "success" };
  return { label: "运行中", tone: "running" };
}

function scheduledTaskPayload(task, enabled = task.enabled) {
  return {
    name: task.name,
    enabled: Boolean(enabled),
    interval_days: Number(task.interval_days),
    run_time: task.run_time,
    phone_number: task.phone_number,
    message: task.message,
  };
}

function scheduledMetaItem(label, value) {
  const item = document.createElement("div");
  const caption = document.createElement("span");
  caption.textContent = label;
  const content = document.createElement("strong");
  content.textContent = value || "--";
  item.append(caption, content);
  return item;
}

async function toggleScheduledTask(task, enabled, checkbox) {
  checkbox.disabled = true;
  try {
    await api(`/api/scheduled-tasks/${encodeURIComponent(task.id)}`, {
      method: "PUT",
      body: JSON.stringify(scheduledTaskPayload(task, enabled)),
    });
    notice(enabled ? "任务已启用" : "任务已暂停");
    scheduledTaskRenderSignature = "";
    await loadScheduledTasks();
  } catch (error) {
    checkbox.checked = !enabled;
    notice(error.message);
  } finally {
    checkbox.disabled = false;
  }
}

async function openScheduledTaskEditor(task = null) {
  const values = await showModal({
    title: task ? "编辑定时任务" : "新建定时任务",
    fields: [
      { name: "name", label: "任务名称", value: task?.name || "", placeholder: "例如：90 天流量查询", required: true, maxLength: 80 },
      { name: "enabled", label: "启用此任务", type: "checkbox", checked: task?.enabled ?? false },
      { name: "interval_days", label: "执行间隔（天）", type: "number", value: task?.interval_days ?? 90, min: 1, max: 3650, required: true },
      { name: "run_time", label: "每天执行时间", type: "time", value: task?.run_time || "08:00", required: true },
      { name: "phone_number", label: "目标号码", type: "tel", value: task?.phone_number || "", placeholder: "10086", required: true, maxLength: 40 },
      { name: "message", label: "短信内容", type: "textarea", value: task?.message || "", placeholder: "短信内容", required: true, maxLength: 1000 },
    ],
    confirmLabel: task ? "保存任务" : "创建任务",
  });
  if (!values) return;
  const payload = {
    name: values.name,
    enabled: Boolean(values.enabled),
    interval_days: Number(values.interval_days),
    run_time: values.run_time,
    phone_number: values.phone_number,
    message: values.message,
  };
  try {
    const path = task ? `/api/scheduled-tasks/${encodeURIComponent(task.id)}` : "/api/scheduled-tasks";
    await api(path, {
      method: task ? "PUT" : "POST",
      body: JSON.stringify(payload),
    });
    notice(task ? "任务已更新" : "任务已创建");
    scheduledTaskRenderSignature = "";
    await loadScheduledTasks();
  } catch (error) {
    notice(error.message);
  }
}

async function runScheduledTaskNow(task) {
  const confirmed = await showModal({
    title: "立即执行任务",
    message: `将立即向 ${task.phone_number} 发送“${task.name}”任务中的短信。`,
    confirmLabel: "立即执行",
  });
  if (!confirmed) return;
  try {
    await api(`/api/scheduled-tasks/${encodeURIComponent(task.id)}/run`, {
      method: "POST",
    });
    notice("任务已开始执行");
    scheduledTaskRenderSignature = "";
    await loadScheduledTasks();
  } catch (error) {
    notice(error.message);
  }
}

async function deleteScheduledTask(task) {
  const confirmed = await showModal({
    title: "删除定时任务",
    message: `确定删除“${task.name}”吗？已有执行历史会保留。`,
    confirmLabel: "删除",
    danger: true,
  });
  if (!confirmed) return;
  try {
    await api(`/api/scheduled-tasks/${encodeURIComponent(task.id)}`, { method: "DELETE" });
    notice("任务已删除");
    scheduledTaskRenderSignature = "";
    await loadScheduledTasks();
  } catch (error) {
    notice(error.message);
  }
}

function renderScheduledTaskCard(task) {
  const card = document.createElement("article");
  card.className = "scheduled-task-card";
  const header = document.createElement("div");
  header.className = "scheduled-task-header";
  const title = document.createElement("div");
  title.className = "scheduled-task-title";
  const name = document.createElement("strong");
  name.textContent = task.name;
  const stateInfo = scheduledTaskState(task);
  const state = document.createElement("span");
  state.className = `scheduled-task-state ${stateInfo.tone}`;
  state.textContent = stateInfo.label;
  title.append(name, state);
  const toggle = document.createElement("label");
  toggle.className = "compact-toggle";
  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.checked = Boolean(task.enabled);
  checkbox.disabled = Boolean(task.running);
  checkbox.setAttribute("aria-label", `${task.enabled ? "暂停" : "启用"}${task.name}`);
  checkbox.addEventListener("change", () => toggleScheduledTask(task, checkbox.checked, checkbox));
  const toggleText = document.createElement("span");
  toggleText.textContent = "启用";
  toggle.append(checkbox, toggleText);
  header.append(title, toggle);

  const body = document.createElement("div");
  body.className = "scheduled-task-body";
  const meta = document.createElement("div");
  meta.className = "scheduled-task-meta";
  meta.append(
    scheduledMetaItem("执行计划", `每 ${task.interval_days} 天 · ${task.run_time}`),
    scheduledMetaItem("目标号码", task.phone_number),
    scheduledMetaItem("下次执行", task.enabled ? formatScheduledTime(task.next_run_at) : "已暂停"),
    scheduledMetaItem("上次执行", formatScheduledTime(task.last_run_at)),
  );
  const message = document.createElement("p");
  message.className = "scheduled-task-message";
  message.textContent = task.message;
  body.append(meta, message);
  if (task.last_error) {
    const error = document.createElement("p");
    error.className = "scheduled-task-error";
    error.textContent = `最近失败：${task.last_error}`;
    body.append(error);
  }

  const actions = document.createElement("div");
  actions.className = "scheduled-task-actions";
  const run = document.createElement("button");
  run.type = "button";
  run.className = "secondary compact";
  run.textContent = "立即执行";
  run.disabled = Boolean(task.running);
  run.addEventListener("click", () => runScheduledTaskNow(task));
  const edit = document.createElement("button");
  edit.type = "button";
  edit.className = "secondary compact";
  edit.textContent = "编辑";
  edit.disabled = Boolean(task.running);
  edit.addEventListener("click", () => openScheduledTaskEditor(task));
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "danger compact";
  remove.textContent = "删除";
  remove.disabled = Boolean(task.running);
  remove.addEventListener("click", () => deleteScheduledTask(task));
  actions.append(run, edit, remove);
  body.append(actions);
  card.append(header, body);
  return card;
}

function renderScheduledRun(run) {
  const row = document.createElement("article");
  row.className = "item";
  const name = document.createElement("strong");
  name.textContent = run.task_name || "已删除任务";
  const detail = document.createElement("p");
  const status = document.createElement("span");
  status.className = `scheduled-run-status ${run.status}`;
  status.textContent = run.status === "success" ? "成功" : "失败";
  const trigger = run.trigger === "manual" ? "立即执行" : "自动执行";
  const result = run.status === "success" ? `${run.segments || 0} 个短信分段` : (run.error || "未知错误");
  detail.append(status, document.createTextNode(` · ${trigger} · ${result}`));
  const time = document.createElement("time");
  time.textContent = formatScheduledTime(run.ended_at);
  row.append(name, detail, time);
  return row;
}

function renderScheduledTasks(snapshot) {
  const signature = JSON.stringify(snapshot);
  if (signature === scheduledTaskRenderSignature) return;
  scheduledTaskRenderSignature = signature;
  const tasks = Array.isArray(snapshot.tasks) ? snapshot.tasks : [];
  const history = Array.isArray(snapshot.history) ? snapshot.history : [];
  const runningCount = tasks.filter((task) => task.running).length;
  const enabledCount = tasks.filter((task) => task.enabled).length;
  $("#scheduled-task-status").textContent = `${tasks.length} 个任务 · ${enabledCount} 个启用${runningCount ? ` · ${runningCount} 个执行中` : ""}`;
  const list = $("#scheduled-task-list");
  if (!tasks.length) {
    list.className = "scheduled-task-grid empty";
    list.textContent = "暂无任务";
  } else {
    list.className = "scheduled-task-grid";
    list.replaceChildren(...tasks.map(renderScheduledTaskCard));
  }
  $("#scheduled-history-count").textContent = `${history.length} 条`;
  const runList = $("#scheduled-run-list");
  if (!history.length) {
    runList.className = "list empty";
    runList.textContent = "暂无执行记录";
  } else {
    runList.className = "list";
    runList.replaceChildren(...history.map(renderScheduledRun));
  }
}

async function loadScheduledTasks() {
  if (scheduledTasksInFlight) return;
  scheduledTasksInFlight = true;
  try {
    renderScheduledTasks(await api("/api/scheduled-tasks"));
  } catch (error) {
    $("#scheduled-task-status").textContent = `读取失败：${error.message}`;
  } finally {
    scheduledTasksInFlight = false;
  }
}

async function openAccountSettings() {
  try {
    const status = await api("/api/auth/status");
    const values = await showModal({
      title: "修改管理员账号",
      fields: [
        { name: "username", label: "账号", value: status.username || "", autocomplete: "username", required: true, maxLength: 64 },
        { name: "current_password", label: "当前密码", type: "password", autocomplete: "current-password", required: true, maxLength: 72 },
        { name: "new_password", label: "新密码（至少 12 字节）", type: "password", autocomplete: "new-password", required: true, maxLength: 72 },
      ],
      confirmLabel: "更新账号",
    });
    if (!values) return;
    await api("/api/auth/credentials", {
      method: "PUT",
      body: JSON.stringify(values),
    });
    window.location.replace("/login");
  } catch (error) {
    notice(error.message);
  }
}

async function logout() {
  try {
    await api("/api/auth/logout", { method: "POST" });
  } finally {
    window.location.replace("/login");
  }
}

function callStateLabel(call) {
  switch (call?.state) {
    case "incoming": return "正在来电";
    case "waiting": return "来电等待";
    case "active": return "通话已接通";
    case "dialing": return "正在拨号";
    case "alerting": return "等待接听";
    case "held": return "通话保持";
    default: return "通话状态";
  }
}

function formatCallDuration(startedAt) {
  const started = new Date(startedAt).getTime();
  if (!Number.isFinite(started)) return "";
  const total = Math.max(0, Math.floor((Date.now() - started) / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  return [hours, minutes, seconds].map((value) => String(value).padStart(2, "0")).join(":");
}

function renderCallHistory(rows) {
  const list = $("#call-history");
  const calls = Array.isArray(rows) ? rows : [];
  const signature = JSON.stringify(calls);
  if (signature === callHistoryRenderSignature) return;
  callHistoryRenderSignature = signature;
  $("#call-history-count").textContent = `${calls.length} 条`;
  if (!calls.length) {
    list.className = "list empty";
    list.textContent = "暂无记录";
    return;
  }
  list.className = "list";
  list.replaceChildren(...calls.map((call) => {
    const row = document.createElement("article");
    row.className = "item call-history-item";
    const number = document.createElement("strong");
    number.textContent = call.number || "未知号码";
    const state = document.createElement("p");
    const endLabels = {
      missed: "未接来电",
      rejected: "已拒接",
      disconnected: "连接中断，结果未知",
      ended: "通话结束",
    };
    state.textContent = endLabels[call.end_reason] || (call.missed ? "未接来电" : "通话结束");
    const time = document.createElement("time");
    time.textContent = new Date(call.started_at).toLocaleString();
    row.append(number, state, time);
    return row;
  }));
}

async function loadCalls() {
  if (callPollInFlight) return;
  callPollInFlight = true;
  try {
    const status = await api("/api/calls/status");
    const active = status.active;
    const panel = $("#active-call");
    const rawIndex = active?.index;
    activeCallIndex = rawIndex === null || rawIndex === undefined || !Number.isInteger(Number(rawIndex)) || Number(rawIndex) <= 0
      ? null
      : Number(rawIndex);
    activeCallState = active?.state || "";
    const interval = Number(status.poll_interval_s || 2);
    $("#call-monitor-status").textContent = status.last_poll_error
      ? `监听异常：${status.last_poll_error}`
      : (status.polling ? `每 ${interval}s 检查来电` : "演示模式");
    const activeSignature = active
      ? JSON.stringify([active.index, active.state, active.number, active.started_at, formatCallDuration(active.started_at)])
      : "none";
    if (activeSignature !== activeCallRenderSignature) {
      activeCallRenderSignature = activeSignature;
      if (active) {
        const ringing = active.state === "incoming" || active.state === "waiting";
        const connected = active.state === "active";
        const ongoing = ["active", "held", "dialing", "alerting"].includes(active.state);
        panel.className = `call-live ${ringing ? "incoming" : (connected ? "active" : "")}`.trim();
        $("#active-call-label").textContent = callStateLabel(active);
        $("#active-call-number").textContent = active.number || "未知号码";
        $("#active-call-time").textContent = `${new Date(active.started_at).toLocaleString()} · ${formatCallDuration(active.started_at)}`;
        $("#answer-call").hidden = !ringing;
        $("#reject-call").hidden = !ringing || activeCallIndex === null;
        $("#hangup-call").hidden = !(ongoing || (ringing && activeCallIndex === null));
      } else {
        panel.className = "call-live";
        $("#active-call-label").textContent = "当前没有通话";
        $("#active-call-number").textContent = "--";
        $("#active-call-time").textContent = "可以拨打新号码";
        $("#answer-call").hidden = true;
        $("#reject-call").hidden = true;
        $("#hangup-call").hidden = true;
      }
    }
    const controlAvailable = Boolean(status.control_available);
    $("#dial-number").disabled = Boolean(active) || !controlAvailable;
    $("#dial-call").disabled = Boolean(active) || !controlAvailable;
    document.querySelectorAll("#dtmf-keypad button").forEach((button) => {
      button.disabled = active?.state !== "active" || !controlAvailable;
    });
    $("#dtmf-status").textContent = active?.state === "active" ? "可以发送按键音" : "通话接通后可用";
    renderCallHistory(status.history);
  } catch (error) {
    $("#call-monitor-status").textContent = `监听异常：${error.message}`;
  } finally {
    callPollInFlight = false;
  }
}

async function runCallControl(button, path, successMessage, body) {
  button.disabled = true;
  try {
    await api(path, {
      method: "POST",
      ...(body ? { body: JSON.stringify(body) } : {}),
    });
    notice(successMessage);
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    await loadCalls();
  }
}

async function probeCallAudio() {
  const button = $("#probe-call-audio");
  button.disabled = true;
  $("#call-audio-state").textContent = "正在检测";
  $("#call-audio-detail").textContent = "正在查询模块音频转发能力。";
  try {
    const result = await api("/api/calls/capabilities", { method: "POST" });
    const bridgeAvailable = Boolean(result.audio_probe_supported && result.host_uac_detected);
    $("#start-call-audio").disabled = !bridgeAvailable || Boolean(callAudioBridge);
    if (result.audio_probe_supported && result.host_uac_detected && result.audio_forwarding && result.audio_mode === "uac") {
      $("#call-audio-state").textContent = "UAC 音频转发已启用";
      $("#call-audio-detail").textContent = "macOS 已识别 BAIWANG 输入与输出设备。";
    } else if (bridgeAvailable) {
      $("#call-audio-state").textContent = "USB 音频设备已就绪";
      $("#call-audio-detail").textContent = "启用后，默认麦克风与扬声器将连接到模块。";
    } else if (result.host_uac_detected) {
      $("#call-audio-state").textContent = "macOS 已识别 UAC";
      $("#call-audio-detail").textContent = "模块未确认 QPCMV 控制，暂不启用音频桥。";
    } else if (result.audio_probe_supported) {
      $("#call-audio-state").textContent = "模块支持 UAC，macOS 未发现设备";
      $("#call-audio-detail").textContent = "当前 USB composition 没有可用的音频输入与输出。";
    } else {
      $("#call-audio-state").textContent = "未发现 USB 音频转发";
      $("#call-audio-detail").textContent = "仍可尝试拨号、接听和 DTMF；当前网页无法承载通话声音。";
    }
  } catch (error) {
    $("#call-audio-state").textContent = "检测失败";
    $("#call-audio-detail").textContent = error.message;
  } finally {
    button.disabled = false;
  }
}

function releaseCallAudioStreams() {
  if (!callAudioBridge) return;
  callAudioBridge.elements.forEach((element) => {
    element.pause();
    element.srcObject = null;
  });
  callAudioBridge.streams.forEach((stream) => stream.getTracks().forEach((track) => track.stop()));
  callAudioBridge = null;
  $("#start-call-audio").disabled = false;
  $("#stop-call-audio").disabled = true;
}

async function startCallAudioBridge() {
  const startButton = $("#start-call-audio");
  startButton.disabled = true;
  $("#call-audio-state").textContent = "正在连接音频";
  let microphoneStream = null;
  let moduleStream = null;
  let moduleEnabled = false;
  try {
    if (!navigator.mediaDevices?.getUserMedia || !("setSinkId" in HTMLMediaElement.prototype)) {
      throw new Error("当前浏览器不支持选择 USB 音频输出");
    }
    microphoneStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const devices = await navigator.mediaDevices.enumerateDevices();
    const moduleInput = devices.find((device) => device.kind === "audioinput" && /AC Interface|BAIWANG/i.test(device.label));
    const moduleOutput = devices.find((device) => device.kind === "audiooutput" && /AS Interface|BAIWANG/i.test(device.label));
    if (!moduleInput || !moduleOutput) throw new Error("浏览器未发现 BAIWANG 音频输入与输出");
    const microphoneLabel = microphoneStream.getAudioTracks()[0]?.label || "";
    if (/AC Interface|BAIWANG/i.test(microphoneLabel)) {
      throw new Error("请先把 macOS 默认麦克风切换为内置或外接麦克风");
    }
    moduleStream = await navigator.mediaDevices.getUserMedia({
      audio: { deviceId: { exact: moduleInput.deviceId } },
    });
    await api("/api/calls/audio/start", { method: "POST" });
    moduleEnabled = true;

    const uplink = new Audio();
    uplink.autoplay = true;
    uplink.srcObject = microphoneStream;
    await uplink.setSinkId(moduleOutput.deviceId);
    await uplink.play();

    const downlink = new Audio();
    downlink.autoplay = true;
    downlink.srcObject = moduleStream;
    await downlink.setSinkId("default");
    await downlink.play();

    callAudioBridge = { streams: [microphoneStream, moduleStream], elements: [uplink, downlink] };
    $("#call-audio-state").textContent = "浏览器音频已连接";
    $("#call-audio-detail").textContent = "默认麦克风 → BAIWANG；BAIWANG → 默认扬声器。";
    $("#stop-call-audio").disabled = false;
  } catch (error) {
    [microphoneStream, moduleStream].filter(Boolean).forEach((stream) => stream.getTracks().forEach((track) => track.stop()));
    if (moduleEnabled) await api("/api/calls/audio/stop", { method: "POST" }).catch(() => {});
    $("#call-audio-state").textContent = "音频连接失败";
    $("#call-audio-detail").textContent = error.message;
    startButton.disabled = false;
  }
}

async function stopCallAudioBridge() {
  const button = $("#stop-call-audio");
  button.disabled = true;
  try {
    await api("/api/calls/audio/stop", { method: "POST" });
  } catch (error) {
    notice(error.message);
  } finally {
    releaseCallAudioStreams();
    $("#call-audio-state").textContent = "浏览器音频已关闭";
    $("#call-audio-detail").textContent = "可以重新检测或启用。";
  }
}

function browserNotificationsSupported() {
  return window.isSecureContext && "Notification" in window;
}

function updateBrowserNotificationStatus() {
  const toggle = $("#browser-notifications");
  const status = $("#browser-notification-status");
  if (!browserNotificationsSupported()) {
    toggle.checked = false;
    toggle.disabled = true;
    status.textContent = "当前浏览器不可用";
    return;
  }
  const enabled = localStorage.getItem("modemcat-browser-notifications") === "true" && Notification.permission === "granted";
  toggle.checked = enabled;
  status.textContent = Notification.permission === "denied"
    ? "浏览器已拒绝权限"
    : (enabled ? "已启用，仅在本页面打开时生效" : "仅在本页面打开时生效");
}

function showBrowserNotification(event) {
  if (!browserNotificationsSupported()) return;
  if (localStorage.getItem("modemcat-browser-notifications") !== "true") return;
  if (Notification.permission !== "granted") return;
  let title = event.title || "ModemCat";
  let body = event.body || "收到新的提醒";
  if (event.kind === "sms" && !$("#include-sms-body").checked) {
    body = `发件人：${event.sender || "未知号码"}`;
  } else if ((event.kind === "incoming_call" || event.kind === "missed_call") &&
    !$("#include-caller-number").checked) {
    body = event.kind === "missed_call" ? "有一个未接来电" : "检测到新的来电";
  }
  try {
    const popup = new Notification(title, {
      body,
      tag: event.id || undefined,
    });
    popup.onclick = () => {
      window.focus();
      popup.close();
    };
  } catch (error) {
    $("#browser-notification-status").textContent = `浏览器提醒失败：${error.message}`;
  }
}

function connectNotificationEvents() {
  if (!("EventSource" in window) || notificationEventSource) return;
  notificationEventSource = new EventSource("/api/events");
  notificationEventSource.onmessage = (message) => {
    let event;
    try {
      event = JSON.parse(message.data);
    } catch (_) {
      return;
    }
    showBrowserNotification(event);
    if (event.kind === "sms") {
      void loadSMS();
    } else if (event.kind === "incoming_call" || event.kind === "missed_call") {
      const includeNumber = $("#include-caller-number").checked;
      notice(includeNumber && event.number ? `${event.title}：${event.number}` : event.title);
      void loadCalls();
    } else if (event.kind === "scheduled_task_success" || event.kind === "scheduled_task_failure") {
      notice(event.title || "定时任务执行完成");
      void loadScheduledTasks();
    }
  };
}

function deliveryStatusText(delivery) {
  if (!delivery) return "尚未发送";
  if (delivery.last_error) return `最近失败：${delivery.last_error}`;
  const lastSuccess = new Date(delivery.last_success || "");
  if (!Number.isNaN(lastSuccess.getTime()) && lastSuccess.getUTCFullYear() > 1) {
    return `最近成功：${lastSuccess.toLocaleString()}`;
  }
  return "尚未发送";
}

// 渠道表单由后端的 channel_types 声明驱动，前端不认识具体渠道，加新渠道不用改这里。
let channelTypes = [];
let channelDrafts = [];

function channelTypeInfo(type) {
  return channelTypes.find((t) => t.type === type);
}

function renderChannelField(channelID, field, settings) {
  const inputID = `ch-${channelID}-${field.name}`;
  const help = field.help ? `<small>${field.help}</small>` : "";
  if (field.kind === "bool") {
    const checked = settings[field.name] ? " checked" : "";
    return `<label class="toggle-row"><input id="${inputID}" type="checkbox"${checked}>` +
      `<span><strong>${field.label}</strong>${help}</span></label>`;
  }
  // 密文字段永远以空值渲染：后端只回传 xxx_configured，留空即保持原值。
  if (field.secret) {
    const configured = settings[`${field.name}_configured`];
    const placeholder = configured ? "留空则保留已保存的值" : (field.placeholder || "");
    return `<label class="span-two"><span>${field.label}${field.required ? " *" : ""}</span>` +
      `<input id="${inputID}" type="password" autocomplete="new-password" placeholder="${placeholder}">` +
      `<small>${configured ? "已配置，留空不改动；清空请填一个空格再保存" : "尚未配置"}${field.help ? " · " + field.help : ""}</small></label>`;
  }
  const value = settings[field.name] == null ? "" : String(settings[field.name]);
  const type = field.kind === "url" ? "url" : "text";
  return `<label><span>${field.label}${field.required ? " *" : ""}</span>` +
    `<input id="${inputID}" type="${type}" autocomplete="off" value="${value}" placeholder="${field.placeholder || ""}">${help}</label>`;
}

function renderChannels() {
  const list = $("#channel-list");
  $("#channel-empty").hidden = channelDrafts.length > 0;
  list.innerHTML = channelDrafts.map((ch) => {
    const info = channelTypeInfo(ch.type);
    if (!info) return "";
    const fields = info.fields.map((f) => renderChannelField(ch.id, f, ch.settings || {}));
    const bools = info.fields.filter((f) => f.kind === "bool");
    const plain = info.fields.filter((f) => f.kind !== "bool");
    return `<section class="settings-section channel-section" data-channel="${ch.id}">
      <div class="settings-heading">
        <div><span class="section-kicker">${info.label}</span>
          <input id="ch-${ch.id}-name" class="channel-name" type="text" value="${ch.name || info.label}" aria-label="备注名"></div>
        <div class="channel-actions">
          <label class="compact-toggle"><input id="ch-${ch.id}-enabled" type="checkbox"${ch.enabled ? " checked" : ""}><span>启用</span></label>
          <button class="secondary compact danger" type="button" data-remove="${ch.id}">删除</button>
        </div>
      </div>
      <div class="settings-grid">${plain.map((f) => renderChannelField(ch.id, f, ch.settings || {})).join("")}</div>
      ${bools.length ? `<div class="toggle-grid compact-grid">${bools.map((f) => renderChannelField(ch.id, f, ch.settings || {})).join("")}</div>` : ""}
      <p class="inline-status">${deliveryStatusText(ch.delivery)}</p>
    </section>`;
  }).join("");

  list.querySelectorAll("[data-remove]").forEach((button) => {
    button.addEventListener("click", () => {
      collectChannelDrafts();
      channelDrafts = channelDrafts.filter((c) => c.id !== button.dataset.remove);
      renderChannels();
    });
  });
}

// 把界面上的值收回草稿。密文输入框留空表示不改动，因此不写进 settings——
// 后端据此沿用原值。
function collectChannelDrafts() {
  channelDrafts = channelDrafts.map((ch) => {
    const info = channelTypeInfo(ch.type);
    if (!info) return ch;
    const settings = {};
    for (const field of info.fields) {
      const el = document.getElementById(`ch-${ch.id}-${field.name}`);
      if (!el) continue;
      if (field.kind === "bool") {
        settings[field.name] = el.checked;
      } else if (field.secret) {
        if (el.value !== "") settings[field.name] = el.value.trim();
      } else {
        settings[field.name] = el.value.trim();
      }
    }
    const nameEl = document.getElementById(`ch-${ch.id}-name`);
    const enabledEl = document.getElementById(`ch-${ch.id}-enabled`);
    return {
      ...ch,
      name: nameEl ? nameEl.value.trim() : ch.name,
      enabled: enabledEl ? enabledEl.checked : ch.enabled,
      settings,
    };
  });
}

async function loadNotificationSettings() {
  if (notificationSettingsInFlight) return;
  notificationSettingsInFlight = true;
  try {
    const settings = await api("/api/notifications");
    $("#notify-sms").checked = Boolean(settings.notify_sms);
    $("#include-sms-body").checked = Boolean(settings.include_sms_body);
    $("#notify-incoming-call").checked = Boolean(settings.notify_incoming_call);
    $("#notify-missed-call").checked = Boolean(settings.notify_missed_call);
    $("#include-caller-number").checked = Boolean(settings.include_caller_number);

    channelTypes = settings.channel_types || [];
    const picker = $("#channel-type");
    if (picker.options.length !== channelTypes.length) {
      picker.innerHTML = channelTypes
        .map((t) => `<option value="${t.type}">${t.label}</option>`)
        .join("");
    }
    channelDrafts = (settings.channels || []).map((c) => ({ ...c }));
    renderChannels();
    updateBrowserNotificationStatus();
  } catch (error) {
    $("#notification-save-status").textContent = `读取设置失败：${error.message}`;
  } finally {
    notificationSettingsInFlight = false;
  }
}

function notificationSettingsPayload() {
  collectChannelDrafts();
  return {
    notify_sms: $("#notify-sms").checked,
    include_sms_body: $("#include-sms-body").checked,
    notify_incoming_call: $("#notify-incoming-call").checked,
    notify_missed_call: $("#notify-missed-call").checked,
    include_caller_number: $("#include-caller-number").checked,
    channels: channelDrafts.map(({ id, type, name, enabled, settings }) => ({
      id, type, name, enabled, settings,
    })),
  };
}

function profileRows(value) {
  const groups = Array.isArray(value) ? value : value?.profiles || [];
  return groups.flatMap((group) =>
    (group.profiles || []).map((profile) => ({ ...profile, aid: group.aid_hex || "" })),
  );
}

function profileDisplayName(profile) {
  return profile?.name || profile?.service_provider_name || profile?.iccid || "未命名 Profile";
}

function activeProfile(profiles) {
  return profiles.find((profile) => profile.state === 1) || null;
}

function maskIdentifier(value, keep = 4) {
  const text = String(value || "");
  if (text.length <= keep * 2) return text;
  return `${text.slice(0, keep)} ${"•".repeat(Math.max(4, text.length - keep * 2))} ${text.slice(-keep)}`;
}

function maskPhoneNumber(value) {
  const text = String(value || "").trim();
  const digitCount = [...text].filter((char) => /\d/.test(char)).length;
  if (digitCount <= 8) return text;
  let digitIndex = 0;
  return [...text].map((char) => {
    if (!/\d/.test(char)) return char;
    digitIndex += 1;
    return digitIndex > 4 && digitIndex <= digitCount - 4 ? "*" : char;
  }).join("");
}

async function copyIdentifier(value, label) {
  try {
    await navigator.clipboard.writeText(value);
    notice(`${label} 已复制`);
  } catch (error) {
    notice(`复制 ${label} 失败，请手动复制`);
  }
}

async function editProfileNote(profile, note) {
  const values = await showModal({
    title: "编辑模块资料",
    message: "这些资料保存在大疆模块中，并按 ICCID 与当前 Profile 关联。",
    confirmLabel: "保存",
    fields: [
      { name: "label", label: "模块内名称", value: note.label || "", placeholder: "可选" },
      { name: "phone", label: "模块号码", value: note.phone || "", placeholder: "可选" },
      { name: "tags", label: "用途标签", value: note.tags || "", placeholder: "例如：英国验证码" },
    ],
  });
  if (!values) return;
  try {
    await api("/api/esim/module-notes", {
      method: "PUT",
      body: JSON.stringify({ iccid: profile.iccid, label: values.label, phone: values.phone, tags: values.tags }),
    });
    notice("模块资料已保存");
    await loadESIM();
  } catch (error) {
    notice(error.message);
  }
}

function phonebookCheck(label, ok, detail) {
  const card = document.createElement("div");
  card.className = `phonebook-check ${ok ? "ok" : ""}`;
  const title = document.createElement("strong");
  title.textContent = label;
  const text = document.createElement("small");
  text.textContent = detail;
  card.append(title, text);
  return card;
}

async function probeESIMPhonebook() {
  const button = $("#probe-esim-phonebook");
  const status = $("#esim-phonebook-status");
  const resultPanel = $("#esim-phonebook-result");
  button.disabled = true;
  status.textContent = "正在检测卡内通讯录能力，不会写入联系人...";
  resultPanel.hidden = true;
  try {
    const result = await api("/api/esim/phonebook/probe", { method: "POST" });
    const supported = result.storage_supported && result.storage_selected;
    const portable = supported && result.read_supported && result.write_supported;
    status.textContent = portable
      ? "已确认当前 Profile 支持卡内通讯录读写；尚未写入任何联系人。"
      : "当前 Profile 未完整确认卡内通讯录读写能力；不会进行写入。";
    resultPanel.replaceChildren(
      phonebookCheck("SIM 通讯录", result.storage_supported, result.storage_supported ? "支持 SM 卡内存储" : "未发现 SM 卡内存储"),
      phonebookCheck("当前卡片", result.storage_selected, result.storage_selected ? "已安全选中 SM 存储" : "无法选中 SM 存储"),
      phonebookCheck("读取能力", result.read_supported, result.read_supported ? "模块支持读取卡内联系人" : "模块未确认读取命令"),
      phonebookCheck("写入接口", result.write_supported, result.write_supported ? "模块声明支持写入接口" : "模块未确认写入命令"),
      phonebookCheck("当前状态", supported, result.storage_status || "未返回容量信息"),
    );
    resultPanel.hidden = false;
  } catch (error) {
    status.textContent = `通讯录检测失败：${error.message}`;
  } finally {
    button.disabled = false;
  }
}

function esimEIDRows(value) {
  const eids = value?.chip_info?.eids;
  return Array.isArray(eids) ? eids : [];
}

function renderESIMChip(overview) {
  const panel = $("#esim-chip");
  const chip = overview?.chip_info || {};
  const eids = esimEIDRows(overview);
  if (!chip.sku_name && !chip.serial_number && !chip.firmware && !eids.length) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }
  panel.hidden = false;
  panel.replaceChildren(
    diagnosticCard("卡类型", chip.sku_name || "eUICC/eSIM 卡片"),
    diagnosticCard("固件", chip.firmware || "--", chip.serial_number ? `序列号 ${chip.serial_number}` : ""),
    diagnosticCard("EID", eids.map((item) => item.eid).filter(Boolean).join(" · ") || "--"),
  );
}

function renderESIMEIDList(overview) {
  const eids = esimEIDRows(overview);
  if (!eids.length) return [];
  return eids.map((item) => {
    const row = document.createElement("article");
    row.className = "item esim-info-row";
    const name = document.createElement("strong");
    name.textContent = "已识别 eUICC";
    const detail = document.createElement("p");
    detail.textContent = [
      item.eid ? `EID ${item.eid}` : "",
      item.aid ? `AID ${item.aid}` : "",
      item.free_nvram ? `可用空间 ${item.free_nvram}` : "",
      item.firmware ? `固件 ${item.firmware}` : "",
    ].filter(Boolean).join("\n");
    const status = document.createElement("small");
    status.textContent = item.spec || item.spec_guess || "eSIM";
    row.append(name, detail, status);
    return row;
  });
}

function renderESIMEIDPanel(rows) {
  if (!rows.length) return null;
  const panel = document.createElement("details");
  panel.className = "esim-euicc-panel";
  const heading = document.createElement("summary");
  heading.className = "esim-euicc-heading";
  const title = document.createElement("strong");
  title.textContent = "已识别 eUICC";
  const hint = document.createElement("small");
  hint.textContent = rows.length > 1 ? `${rows.length} 张 eSIM 卡片` : "卡片信息";
  heading.append(title, hint);
  panel.append(heading, ...rows);
  return panel;
}

async function loadESIMHealth() {
  if (esimHealthInFlight) return;
  esimHealthInFlight = true;
  const section = $("#esim-runtime-section");
  const panel = $("#esim-runtime");
  section.hidden = false;
  panel.replaceChildren(diagnosticCard("Profile 检查", "正在检测"));
  try {
    const health = await api("/api/esim/health");
    if (health.card_type === "physical_sim") {
      section.hidden = true;
      return;
    }
    if (!health.active_profile) {
      panel.replaceChildren(diagnosticCard("Profile 检查", health.message || "未发现已启用 Profile"));
      return;
    }
    const profile = health.active_profile;
    const signal = Number.isFinite(health.signal_dbm) ? `${health.signal_dbm} dBm` : "--";
    panel.replaceChildren(
      diagnosticCard("当前启用", profileDisplayName(profile), profile.iccid ? `ICCID ${maskIdentifier(profile.iccid)}` : ""),
      diagnosticCard("模块实际卡", health.module_iccid ? maskIdentifier(health.module_iccid) : "--", health.imsi ? `IMSI ${health.imsi}` : ""),
      diagnosticCard("蜂窝注册", health.registration || "未注册", [displayOperatorName(health.operator), health.network_mode].filter(Boolean).join(" · ")),
      diagnosticCard("信号", signal, health.registered ? "模块已接管当前 Profile" : "等待网络注册"),
    );
  } catch (error) {
    panel.replaceChildren(diagnosticCard("Profile 检查", "暂时无法读取", error.message));
  } finally {
    esimHealthInFlight = false;
  }
}

function setESIMHealthPolling(enabled) {
  clearInterval(esimHealthPollTimer);
  esimHealthPollTimer = null;
  if (!enabled) return;
  esimHealthPollTimer = setInterval(() => {
    if ($("#esim").classList.contains("active")) void loadESIMHealth();
  }, 30000);
}

function diagnosticCard(label, value, detail = "") {
  const card = document.createElement("div");
  card.className = "diagnostic-card";
  const span = document.createElement("span");
  span.textContent = label;
  const strong = document.createElement("strong");
  strong.textContent = value || "--";
  card.append(span, strong);
  if (detail) {
    const small = document.createElement("small");
    small.textContent = detail;
    card.append(small);
  }
  return card;
}

function renderNetworkCheck(label, result) {
  const list = $("#network-checks");
  list.className = "list";
  const row = document.createElement("article");
  row.className = `item check-item ${result.ok ? "ok" : "bad"}`;
  const name = document.createElement("strong");
  name.textContent = label;
  const detail = document.createElement("p");
  detail.textContent = result.detail || result.summary || "";
  const status = document.createElement("small");
  status.textContent = result.ok ? "通过" : "未通过";
  row.append(name, detail, status);
  const existing = [...list.querySelectorAll(".item")].filter((item) => item.dataset.label !== label);
  row.dataset.label = label;
  list.replaceChildren(row, ...existing);
}

async function runNetworkCheck(label, path, button) {
  button.disabled = true;
  try {
    const result = await api(path, { method: "POST" });
    renderNetworkCheck(label, result);
    notice(result.summary || "检测完成");
  } catch (error) {
    renderNetworkCheck(label, { ok: false, summary: "检测失败", detail: error.message });
    notice(error.message);
  } finally {
    button.disabled = false;
  }
}

async function loadNetwork() {
  const grid = $("#network-grid");
  const ifaceList = $("#network-interfaces");
  $("#network-status").textContent = "正在读取网络诊断...";
  try {
    const diag = await api("/api/network");
    const active = Array.isArray(diag.active_contexts) ? diag.active_contexts.join(", ") : "";
    const apns = Array.isArray(diag.pdp_contexts)
      ? diag.pdp_contexts.map((ctx) => `${ctx.id}:${ctx.apn}`).join(" · ")
      : "";
    const addresses = Array.isArray(diag.pdp_addresses) ? diag.pdp_addresses.join(" · ") : "";
    const usb = diag.usb_device
      ? `${diag.usb_device.vendor || ""} ${diag.usb_device.product || ""} (${diag.usb_device.vendor_id}:${diag.usb_device.product_id})`
      : "未检测到";
    const route = diag.default_route || {};
    const routeText = route.interface
      ? `${route.interface}${route.gateway ? ` -> ${route.gateway}` : ""}`
      : "未知";
    grid.replaceChildren(
      diagnosticCard("USB 网卡", diag.usb_network_supported === false ? "固件不支持" : (diag.usb_network_present ? "已识别" : "未识别"), "macOS 是否出现可用 USB 网络接口"),
      diagnosticCard("默认出口", routeText, "当前 macOS 实际优先使用的网卡和网关"),
	  diagnosticCard(diag.module_family === "airm2m" ? "SETUSB" : "usbnet", diag.usbnet_mode || "未知", "模块当前 USB 网络模式"),
      diagnosticCard("蜂窝数据", active ? `已激活 ${active}` : "未激活", "PDP context 激活状态"),
      diagnosticCard("蜂窝 IP", addresses || "无", "模块侧拿到的数据网络地址"),
      diagnosticCard("APN", apns || "无", "当前可见 PDP 配置"),
      diagnosticCard("USB 枚举", usb, diag.usb_device?.mode || ""),
    );

    const errorText = diag.errors ? ` · 错误：${Object.values(diag.errors).join("；")}` : "";
    $("#network-status").textContent = diag.usb_network_supported === false
      ? `Air780 ${diag.firmware_flavor || "当前"} 固件不提供 USB 网卡${errorText}`
      : (diag.usb_network_present
        ? `macOS 已识别 USB 网络接口${errorText}`
        : `蜂窝侧可能已通，但 macOS 尚未识别 USB 网卡${errorText}`);

    const interfaces = Array.isArray(diag.mac_interfaces) ? diag.mac_interfaces : [];
    if (!interfaces.length) {
      ifaceList.className = "list empty";
      ifaceList.textContent = "未读取到网络接口";
      return;
    }
    ifaceList.className = "list";
    ifaceList.replaceChildren(...interfaces.map((item) => {
      const row = document.createElement("article");
      row.className = "item";
      const name = document.createElement("strong");
      name.textContent = item.name;
      const detail = document.createElement("p");
      detail.textContent = [item.kind, item.status, item.ipv4].filter(Boolean).join(" · ");
      const status = document.createElement("small");
      status.textContent = item.status === "active" ? "active" : "inactive";
      row.append(name, detail, status);
      return row;
    }));
  } catch (error) {
    $("#network-status").textContent = `读取网络诊断失败：${error.message}`;
    grid.replaceChildren();
    ifaceList.className = "list empty";
    ifaceList.textContent = "读取失败";
    notice(error.message);
  }
}

function formatTrafficBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  const units = ["B", "KB", "MB", "GB", "TB"];
  let amount = bytes;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : (amount >= 100 ? 0 : amount >= 10 ? 1 : 2);
  return `${amount.toFixed(digits)} ${units[unit]}`;
}

async function loadNetworkTraffic() {
  // 流量只显示在「模组与网络」页，其它页面或窗口不可见时没必要每秒打一次接口。
  // 重置基准是因为速率按两次采样的时间差算，跨越长间隔会平均成误导性的数字。
  if (pollSuspended || document.hidden || activeViewID() !== "network") {
    networkTrafficPrevious = null;
    return;
  }
  if (networkTrafficInFlight) return;
  networkTrafficInFlight = true;
  try {
    const sample = await api("/api/network/traffic");
    if (!sample.available) {
      networkTrafficPrevious = null;
      setValue("#traffic-rx-rate", "--", "muted");
      setValue("#traffic-tx-rate", "--", "muted");
      setValue("#traffic-session-rx", "--", "muted");
      setValue("#traffic-session-tx", "--", "muted");
      setValue("#traffic-session-total", "--", "muted");
	  $("#traffic-session-total").title = sample.error || "未检测到 Baiwang USB 网卡";
      return;
    }

    let rxRate = 0;
    let txRate = 0;
    const previous = networkTrafficPrevious;
    if (previous && previous.interface === sample.interface) {
      const elapsed = (Number(sample.sampled_at_ms) - Number(previous.sampled_at_ms)) / 1000;
      if (elapsed > 0) {
        rxRate = Math.max(0, Number(sample.rx_bytes) - Number(previous.rx_bytes)) / elapsed;
        txRate = Math.max(0, Number(sample.tx_bytes) - Number(previous.tx_bytes)) / elapsed;
      }
    }
    networkTrafficPrevious = sample;
    setValue("#traffic-rx-rate", `${formatTrafficBytes(rxRate)}/s`, "neutral");
    setValue("#traffic-tx-rate", `${formatTrafficBytes(txRate)}/s`, "neutral");
    setValue("#traffic-session-rx", formatTrafficBytes(sample.session_rx_bytes), "neutral");
    setValue("#traffic-session-tx", formatTrafficBytes(sample.session_tx_bytes), "neutral");
    setValue("#traffic-session-total", formatTrafficBytes(sample.session_total_bytes), "emphasis");
    $("#traffic-session-total").title = "本次启动期间的下载与上传流量之和；关闭 ModemCat 后清零";
  } catch (error) {
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-total", "--", "muted");
  } finally {
    networkTrafficInFlight = false;
  }
}

function setNetworkTrafficPolling(enabled) {
  clearInterval(networkTrafficTimer);
  networkTrafficTimer = null;
  if (!enabled) {
    networkTrafficPrevious = null;
    return;
  }
  void loadNetworkTraffic();
  networkTrafficTimer = setInterval(loadNetworkTraffic, 1000);
}

async function setUSBNetMode(mode) {
  const label = `模式 ${mode}`;
	const settingName = currentModuleFamily === "airm2m" ? "SETUSB" : "usbnet";
  const confirmed = await showModal({
    title: `切换到${label}`,
	message: `将写入 ${settingName}=${mode}，重启模块后生效。`,
    confirmLabel: "继续切换",
  });
  if (!confirmed) return;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
	notice(result.needs_reboot ? `${settingName} 已写入 ${result.mode}，请重启模块` : `当前已经是 ${settingName}=${result.mode}`);
    await loadNetwork();
  } catch (error) {
    notice(error.message);
  }
}

async function switchWorkMode(mode, label, button) {
	const settingName = currentModuleFamily === "airm2m" ? "SETUSB" : "usbnet";
  const confirmed = await showModal({
    title: `切换到${label}`,
	message: `将写入 ${settingName}=${mode} 并重启模块，USB 会短暂断开。`,
    confirmLabel: "确认切换",
  });
  if (!confirmed) return;
  const status = $("#workmode-status");
  const buttons = [$("#workmode-sms"), $("#workmode-network")];
  buttons.forEach((item) => { item.disabled = true; });
  status.hidden = false;
  status.textContent = `正在切到${label}...`;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
    if (!result.needs_reboot) {
      status.textContent = `当前已经是${label}，无需重启。`;
      buttons.forEach((item) => { item.disabled = false; });
      await loadStatus();
      return;
    }
	status.textContent = `${settingName} 已写入 ${result.mode}，正在重启模块...`;
    await api("/api/network/reboot-module", { method: "POST" });
    notice(`${label}切换中`);
    const back = await waitForModule((seconds) => {
      status.textContent = `${label}已写入，等待模块重新枚举…（${seconds} 秒）`;
    });
    status.textContent = back
      ? `${label}切换完成，模块已重新上线。`
      : `${label}已写入，但模块超时未重新枚举，请检查连接或手动拔插。`;
    buttons.forEach((item) => { item.disabled = false; });
    if (back) await loadNetwork();
  } catch (error) {
    status.hidden = false;
    status.textContent = `${label}切换失败：${error.message}`;
    notice(error.message);
    buttons.forEach((item) => { item.disabled = false; });
  }
}

async function rebootModule() {
  const confirmed = await showModal({
    title: "重启模块",
    message: "模块会重新枚举 USB，网页可能短暂断开。",
    confirmLabel: "确认重启",
  });
  if (!confirmed) return;
  try {
    await api("/api/network/reboot-module", { method: "POST" });
    notice("模块正在重启");
    const back = await waitForModule();
    notice(back ? "模块已重新上线" : "模块超时未重新枚举，请检查连接");
    if (back) await loadNetwork();
  } catch (error) {
    notice(error.message);
  }
}

// waitForModule 轮询到模块重新枚举为止。
//
// 实测这块模块 AT+CFUN=1,1 之后重新枚举约需 20 秒，此前用的是 8/12/13 秒固定超时，
// 结果是界面先报两次失败、再宣布"切换完成"，而设备其实还没回来。改成轮询到就绪，
// 并把耗时反馈给用户。重启期间挂起后台轮询，否则它们只会往一个不存在的设备上打。
async function waitForModule(onTick, budgetMs = 60000) {
  pollSuspended = true;
  const started = Date.now();
  try {
    while (Date.now() - started < budgetMs) {
      const seconds = Math.round((Date.now() - started) / 1000);
      if (onTick) onTick(seconds);
      await new Promise((resolve) => setTimeout(resolve, 2000));
      try {
        await loadStatus();
        return true;
      } catch {
        // 设备还没回来，继续等
      }
    }
    return false;
  } finally {
    pollSuspended = false;
  }
}

async function loadESIM() {
  const list = $("#esim-list");
  const status = $("#esim-status");
  const download = $("#esim-download-section");
  const runtime = $("#esim-runtime-section");
  const profilePanel = $("#esim-profile-panel");
  const phonebook = $("#esim-phonebook-section");
  $("#esim-chip").hidden = true;
  $("#esim-chip").replaceChildren();
  runtime.hidden = true;
  download.hidden = false;
  profilePanel.hidden = false;
  phonebook.hidden = false;
  list.className = "list empty";
  list.textContent = "正在读取 eUICC";
  status.textContent = "正在通过 AT+CCHO/CGLA 读取 eUICC/eSIM 卡片";
  try {
    const overview = await api("/api/esim");
    if (overview.card_type === "physical_sim") {
      status.textContent = overview.message;
      list.textContent = overview.message;
      download.hidden = true;
      profilePanel.hidden = true;
      phonebook.hidden = true;
      setESIMHealthPolling(false);
      return;
    }
    const notesResponse = await api("/api/esim/module-notes");
    const notes = notesResponse.notes || {};
    const profiles = profileRows(overview);
    const eidRows = renderESIMEIDList(overview);
    const eidPanel = renderESIMEIDPanel(eidRows);
    renderESIMChip(overview);
    const profileCount = profiles.length;
    const eidCount = esimEIDRows(overview).length;
    const active = activeProfile(profiles);
    status.textContent = active
      ? `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 当前使用 ${profileDisplayName(active)}`
      : `已读取：${eidCount} 个 eUICC，${profileCount} 个 Profile · 未发现已启用 Profile`;
    if (!profiles.length) {
      if (eidRows.length) {
        list.className = "list";
        list.replaceChildren(eidPanel);
        return;
      }
      list.textContent = "未发现 eUICC/eSIM 卡片参数";
      return;
    }
    list.className = "list";
    const profileItems = profiles.map((profile) => {
      const note = notes[profile.iccid] || {};
      const row = document.createElement("article");
      row.className = `item esim-profile ${profile.state === 1 ? "active" : ""}`;
      const name = document.createElement("strong");
      name.textContent = note.label || profileDisplayName(profile);
      const detail = document.createElement("p");
      detail.textContent = [
        note.label && note.label !== profileDisplayName(profile) ? `卡内名称：${profileDisplayName(profile)}` : "",
        profile.service_provider_name ? `服务商：${profile.service_provider_name}` : "",
        profile.class_text ? `类型：${profile.class_text}` : "",
        note.tags ? `标签：${note.tags}` : "",
      ].filter(Boolean).join("\n");
      const metadata = document.createElement("div");
      metadata.className = "profile-metadata";
      if (note.phone) {
        const phoneRow = document.createElement("div");
        phoneRow.className = "profile-identifier-row";
        const phone = document.createElement("code");
        phone.className = "profile-iccid";
        phone.textContent = `模块号码 ${maskPhoneNumber(note.phone)}`;
        const revealPhone = document.createElement("button");
        revealPhone.className = "secondary compact profile-toggle-button";
        revealPhone.type = "button";
        revealPhone.textContent = "显示";
        revealPhone.addEventListener("click", () => {
          const hidden = revealPhone.textContent === "显示";
          phone.textContent = `模块号码 ${hidden ? note.phone : maskPhoneNumber(note.phone)}`;
          revealPhone.textContent = hidden ? "隐藏" : "显示";
        });
        const copyPhone = document.createElement("button");
        copyPhone.className = "secondary compact profile-copy-button";
        copyPhone.type = "button";
        copyPhone.textContent = "复制号码";
        copyPhone.addEventListener("click", () => copyIdentifier(note.phone, "模块号码"));
        phoneRow.append(phone, revealPhone, copyPhone);
        metadata.append(phoneRow);
      }
      if (profile.iccid) {
        const iccidRow = document.createElement("div");
        iccidRow.className = "profile-identifier-row";
        const iccid = document.createElement("code");
        iccid.className = "profile-iccid";
        iccid.textContent = `ICCID ${maskIdentifier(profile.iccid)}`;
        const reveal = document.createElement("button");
        reveal.className = "secondary compact profile-toggle-button";
        reveal.type = "button";
        reveal.textContent = "显示";
        reveal.addEventListener("click", () => {
          const hidden = reveal.textContent === "显示";
          iccid.textContent = `ICCID ${hidden ? profile.iccid : maskIdentifier(profile.iccid)}`;
          reveal.textContent = hidden ? "隐藏" : "显示";
        });
        const copy = document.createElement("button");
        copy.className = "secondary compact profile-copy-button";
        copy.type = "button";
        copy.textContent = "复制 ICCID";
        copy.addEventListener("click", () => copyIdentifier(profile.iccid, "ICCID"));
        iccidRow.append(iccid, reveal, copy);
        metadata.append(iccidRow);
      }
      const actionBox = document.createElement("div");
      actionBox.className = "profile-actions";
      if (profile.state !== 1) {
        const button = document.createElement("button");
        button.className = "compact";
        button.textContent = "启用";
        button.addEventListener("click", async () => {
          const label = profileDisplayName(profile);
          const confirmed = await showModal({
            title: "启用 Profile",
            message: `确定启用 ${label} 吗？当前正在使用的 eSIM Profile 会被切换。`,
            confirmLabel: "启用",
          });
          if (!confirmed) {
            return;
          }
          button.disabled = true;
          button.textContent = "切换中";
          try {
            const result = await api("/api/esim/switch", {
              method: "POST",
              body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }),
            });
            if (result.module_reboot_requested) {
              status.textContent = `已切换到 ${label}；模块正在重启，等待新 Profile 接管（约 ${result.reconnect_wait_seconds || 10} 秒）`;
              notice(`已切换 ${label}，模块正在重新读取新卡`);
              setTimeout(async () => {
                await loadESIM();
                await loadStatus();
              }, (result.reconnect_wait_seconds || 10) * 1000);
            } else {
              status.textContent = `Profile 已切换到 ${label}，但模块重启未确认：${result.module_reboot_warning || "请手动重启后再读取号码"}`;
              notice("Profile 已切换，模块重启未确认");
              await loadESIM();
            }
          } catch (error) {
            status.textContent = `切换失败：${error.message}`;
            notice(error.message);
            button.disabled = false;
            button.textContent = "启用";
          }
        });
        actionBox.append(button);
      } else {
        const button = document.createElement("button");
        button.className = "secondary compact";
        button.type = "button";
        button.textContent = "启用";
        button.disabled = true;
        actionBox.append(button);
      }
      const rename = document.createElement("button");
      rename.className = "secondary compact";
      rename.type = "button";
      rename.textContent = "改名";
      rename.addEventListener("click", async () => {
        const values = await showModal({
          title: "修改 Profile 名称",
          message: "名称将写入 eUICC 卡片内部的 Profile nickname。",
          confirmLabel: "保存",
          fields: [{ name: "name", label: "Profile 名称", value: profileDisplayName(profile), required: true }],
        });
        if (!values?.name) return;
        rename.disabled = true;
        try {
          await api("/api/esim/profile", { method: "PATCH", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "", name: values.name }) });
          notice("Profile 名称已修改");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { rename.disabled = false; }
      });
      const localNote = document.createElement("button");
      localNote.className = "secondary compact";
      localNote.type = "button";
      localNote.textContent = "模块资料";
      localNote.addEventListener("click", () => editProfileNote(profile, note));
      const remove = document.createElement("button");
      remove.className = "secondary danger compact";
      remove.type = "button";
      remove.textContent = "删除";
      remove.disabled = profile.state === 1;
      remove.addEventListener("click", async () => {
        const last4 = String(profile.iccid || "").slice(-4);
        const values = await showModal({
          title: "删除 Profile",
          message: `删除不可恢复。请输入 ICCID 后四位 ${last4} 确认。`,
          confirmLabel: "删除",
          danger: true,
          fields: [{ name: "confirmation", label: "ICCID 后四位", required: true }],
        });
        if (!values) return;
        if (values.confirmation !== last4) {
          notice("ICCID 后四位不匹配，未执行删除");
          return;
        }
        remove.disabled = true;
        try {
          await api("/api/esim/profile", { method: "DELETE", body: JSON.stringify({ iccid: profile.iccid, aid: profile.aid || "" }) });
          notice("Profile 已删除");
          await loadESIM();
        } catch (error) { notice(error.message); } finally { remove.disabled = false; }
      });
      actionBox.append(localNote, rename, remove);
      const description = document.createElement("div");
      description.className = "profile-description";
      description.append(detail, metadata);
      row.append(name, description, actionBox);
      return row;
    });
    list.replaceChildren(...(eidPanel ? [eidPanel] : []), ...profileItems);
    void loadESIMHealth();
    setESIMHealthPolling(true);
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
    list.textContent = error.message;
    setESIMHealthPolling(false);
  }
}

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab, .view").forEach((el) => el.classList.remove("active"));
    tab.classList.add("active");
    $(`#${tab.dataset.view}`).classList.add("active");
    $("#page-title").textContent = tab.querySelector("span").textContent;
    if (tab.dataset.view === "esim") loadESIM();
    else setESIMHealthPolling(false);
    if (tab.dataset.view === "network") loadNetwork();
    if (tab.dataset.view === "notifications") loadNotificationSettings();
    if (tab.dataset.view === "scheduled") loadScheduledTasks();
    if (tab.dataset.view === "calls") loadCalls();
  });
});

$("#esim-download-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const confirmed = await showModal({
    title: "下载新的 Profile",
    message: "将向 SM-DP+ 服务器下载并写入新的 eSIM Profile。写入期间请勿拔出模块。",
    confirmLabel: "开始下载",
  });
  if (!confirmed) return;
  const button = event.currentTarget.querySelector("button[type=submit]");
  const status = $("#esim-download-status");
  button.disabled = true;
  status.textContent = "正在下载并写入 Profile，请勿拔出模块...";
  try {
    const result = await api("/api/esim/download", { method: "POST", body: JSON.stringify({
      smdp: $("#esim-smdp").value, matching_id: $("#esim-matching-id").value,
      confirmation_code: $("#esim-confirmation-code").value, imei: $("#esim-imei").value, aid: $("#esim-aid").value,
    }) });
    status.textContent = result.message || "Profile 下载完成，正在重新读取卡片";
    notice("Profile 下载完成");
    await loadESIM();
  } catch (error) { status.textContent = `下载失败：${error.message}`; notice(error.message); } finally { button.disabled = false; }
});

$("#send-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  const originalLabel = button.textContent;
  button.disabled = true;
  button.textContent = "发送中";
  try {
    const result = await api("/api/sms/send", {
      method: "POST",
      body: JSON.stringify({ phone: $("#phone").value, message: $("#message").value }),
    });
    $("#message").value = "";
    const segments = Number(result.segments || 1);
    notice(segments > 1 ? `短信已发送（${segments} 个分片）` : "短信已发送");
    await loadSMS();
    $("#message").focus();
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = originalLabel;
  }
});

$("#new-conversation").addEventListener("click", async () => {
  const values = await showModal({
    title: "新建短信",
    fields: [{ name: "phone", label: "接收号码", type: "tel", autocomplete: "tel", required: true }],
    confirmLabel: "开始对话",
  });
  if (!values?.phone) return;
  selectSMSConversation(values.phone, true);
});

$("#conversation-back").addEventListener("click", () => {
  $(".sms-workspace").classList.remove("thread-open");
});

$("#dial-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  await runCallControl($("#dial-call"), "/api/calls/dial", "拨号指令已发送", {
    number: $("#dial-number").value,
  });
});

$("#at-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const output = $("#at-output");
  output.textContent = "执行中";
  try {
    const result = await api("/api/at", {
      method: "POST",
      body: JSON.stringify({ command: $("#at-command").value }),
    });
    output.textContent = result.response || "OK";
  } catch (error) {
    output.textContent = error.message;
  }
});

$("#refresh").addEventListener("click", async () => {
  await Promise.all([loadStatus(), loadSMS()]);
  notice("状态已刷新");
});
$("#refresh-sms").addEventListener("click", async () => {
  const button = $("#refresh-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在读取短信...";
  try {
    const result = await api("/api/sms/refresh", { method: "POST" });
    await loadSMS();
    $("#sms-status").textContent = `短信读取完成：${result.count ?? "未知"} 条`;
    notice("短信读取完成");
  } catch (error) {
    $("#sms-status").textContent = `读取短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#refresh-calls").addEventListener("click", loadCalls);
$("#new-scheduled-task").addEventListener("click", () => openScheduledTaskEditor());
$("#refresh-scheduled-tasks").addEventListener("click", async () => {
  const button = $("#refresh-scheduled-tasks");
  button.disabled = true;
  try {
    scheduledTaskRenderSignature = "";
    await loadScheduledTasks();
    notice("定时任务已刷新");
  } finally {
    button.disabled = false;
  }
});
$("#clear-module-sms").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "清空模块旧短信",
    message: "只会清空模块内部 ME 存储里的旧短信，不会删除 SIM 卡短信或本地已保存的短信历史。",
    confirmLabel: "确认清空",
    danger: true,
  });
  if (!confirmed) return;
  const button = $("#clear-module-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在清空模块内部旧短信...";
  try {
    const result = await api("/api/sms/clear-module", { method: "POST" });
    $("#sms-status").textContent = `模块旧短信已清理：${result.before ?? 0} -> ${result.after ?? 0} 条`;
    await loadSMS();
    notice("模块旧短信已清理");
  } catch (error) {
    $("#sms-status").textContent = `清理模块旧短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#refresh-esim").addEventListener("click", loadESIM);
$("#account-settings").addEventListener("click", openAccountSettings);
$("#logout").addEventListener("click", logout);
$("#probe-esim-phonebook").addEventListener("click", probeESIMPhonebook);
$("#refresh-network").addEventListener("click", loadNetwork);
$("#workmode-sms").addEventListener("click", () =>
	switchWorkMode(0, "短信模式", $("#workmode-sms")));
$("#workmode-network").addEventListener("click", () =>
	switchWorkMode(currentModuleFamily === "airm2m" ? 2 : 1, "上网模式", $("#workmode-network")));
$("#check-4g-route").addEventListener("click", () =>
  runNetworkCheck("4G 出口", "/api/network/check-4g", $("#check-4g-route")));
$("#check-proxy-route").addEventListener("click", () =>
  runNetworkCheck("代理", "/api/network/check-proxy", $("#check-proxy-route")));
$("#usbnet-mode-0").addEventListener("click", () => setUSBNetMode(0));
$("#usbnet-mode-1").addEventListener("click", () => setUSBNetMode(currentModuleFamily === "airm2m" ? 2 : 1));
$("#reboot-module").addEventListener("click", rebootModule);
$("#sms-reachability-reboot").addEventListener("click", rebootModule);
$("#add-channel").addEventListener("click", () => {
  const type = $("#channel-type").value;
  const info = channelTypeInfo(type);
  if (!info) return;
  collectChannelDrafts();
  channelDrafts.push({
    id: `new-${Date.now().toString(36)}`,
    type,
    name: info.label,
    enabled: false,
    settings: {},
  });
  renderChannels();
});

$("#reject-call").addEventListener("click", async () => {
  const button = $("#reject-call");
  const index = activeCallIndex;
  if (!Number.isInteger(index)) {
    notice("当前来电已结束");
    return;
  }
  button.disabled = true;
  try {
    await api("/api/calls/reject", {
      method: "POST",
      body: JSON.stringify({ index }),
    });
    notice("已发送拒接指令");
    await loadCalls();
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});

$("#answer-call").addEventListener("click", () =>
  runCallControl($("#answer-call"), "/api/calls/answer", "已发送接听指令"));

$("#hangup-call").addEventListener("click", () =>
  runCallControl($("#hangup-call"), "/api/calls/hangup", "已发送挂断指令"));

document.querySelectorAll("#dtmf-keypad button").forEach((button) => {
  button.addEventListener("click", () =>
    runCallControl(button, "/api/calls/dtmf", `已发送按键 ${button.dataset.tone}`, { tone: button.dataset.tone }));
});

$("#probe-call-audio").addEventListener("click", probeCallAudio);
$("#start-call-audio").addEventListener("click", startCallAudioBridge);
$("#stop-call-audio").addEventListener("click", stopCallAudioBridge);

function setNotificationActionsDisabled(disabled) {
  notificationActionInFlight = disabled;
  $("#save-notifications").disabled = disabled;
  $("#test-notifications").disabled = disabled;
}

function clearNotificationCredentialInputs() {
  $("#bark-push-url").value = "";
  $("#telegram-bot-token").value = "";
  $("#bark-clear-url").checked = false;
  $("#telegram-clear-token").checked = false;
}

$("#browser-notifications").addEventListener("change", async (event) => {
  if (!event.currentTarget.checked) {
    localStorage.setItem("modemcat-browser-notifications", "false");
    updateBrowserNotificationStatus();
    return;
  }
  if (!browserNotificationsSupported()) {
    updateBrowserNotificationStatus();
    return;
  }
  const permission = await Notification.requestPermission();
  localStorage.setItem("modemcat-browser-notifications", String(permission === "granted"));
  updateBrowserNotificationStatus();
});

$("#notification-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (notificationActionInFlight) return;
  setNotificationActionsDisabled(true);
  $("#notification-save-status").textContent = "正在保存...";
  try {
    await api("/api/notifications", {
      method: "PUT",
      body: JSON.stringify(notificationSettingsPayload()),
    });
    clearNotificationCredentialInputs();
    $("#notification-save-status").textContent = "提醒设置已保存";
    notice("提醒设置已保存");
    await loadNotificationSettings();
  } catch (error) {
    $("#notification-save-status").textContent = `保存失败：${error.message}`;
    notice(error.message);
  } finally {
    setNotificationActionsDisabled(false);
  }
});

$("#test-notifications").addEventListener("click", async () => {
  if (notificationActionInFlight) return;
  let settingsSaved = false;
  setNotificationActionsDisabled(true);
  $("#notification-save-status").textContent = "正在保存提醒设置...";
  try {
    await api("/api/notifications", {
      method: "PUT",
      body: JSON.stringify(notificationSettingsPayload()),
    });
    settingsSaved = true;
    clearNotificationCredentialInputs();
    $("#notification-save-status").textContent = "设置已保存，正在发送测试提醒...";
    const result = await api("/api/notifications/test", { method: "POST" });
    const summary = Object.entries(result.results || {}).map(([channel, status]) => `${channel}: ${status}`).join(" · ");
    await loadNotificationSettings();
    const message = summary || "测试提醒已发送";
    $("#notification-save-status").textContent = `设置已保存；测试结果：${message}`;
    notice(message);
  } catch (error) {
    if (settingsSaved) {
      await loadNotificationSettings();
      $("#notification-save-status").textContent = `设置已保存，但测试失败：${error.message}`;
    } else {
      $("#notification-save-status").textContent = `保存失败：${error.message}`;
    }
    notice(settingsSaved ? `测试失败：${error.message}` : error.message);
  } finally {
    setNotificationActionsDisabled(false);
  }
});

loadStatus();
loadSMS();
loadScheduledTasks();
loadCalls();
loadNotificationSettings();
updateBrowserNotificationStatus();
connectNotificationEvents();
setNetworkTrafficPolling(true);
// AT 口是单一串行资源，后台轮询会和用户正在做的操作抢同一个口。所以只轮询当前视图
// 真正需要的数据，窗口不可见时全停，连续失败则退避，模块重启期间由 waitForModule 挂起。
function activeViewID() {
  const el = document.querySelector(".view.active");
  return el ? el.id : "overview";
}

function startPoller({ run, every, views, max = 60000 }) {
  let delay = every;
  const tick = async () => {
    const needed = !views || views.includes(activeViewID());
    if (!pollSuspended && !document.hidden && needed) {
      try {
        await run();
        delay = every;
      } catch {
        delay = Math.min(delay * 2, max); // 设备离线时别继续满速重试
      }
    }
    setTimeout(tick, delay);
  };
  setTimeout(tick, delay);
}

// views 为 null 表示所有视图都需要（顶部状态条常驻）
startPoller({ run: loadStatus, every: 10000, views: null });
startPoller({ run: loadSMS, every: 5000, views: ["sms", "overview"] });
startPoller({ run: loadScheduledTasks, every: 5000, views: ["scheduled"] });
startPoller({ run: loadCalls, every: 2000, views: ["calls"] });

// 切回页面时立刻刷新一次，不用等下一个周期
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && !pollSuspended) loadStatus();
});
