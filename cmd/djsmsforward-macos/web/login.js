const authForm = document.querySelector("#auth-form");
const authTitle = document.querySelector("#auth-title");
const authSubtitle = document.querySelector("#auth-subtitle");
const authUsername = document.querySelector("#auth-username");
const authPassword = document.querySelector("#auth-password");
const authConfirm = document.querySelector("#auth-confirm");
const authConfirmRow = document.querySelector("#auth-confirm-row");
const authSubmit = document.querySelector("#auth-submit");
const authError = document.querySelector("#auth-error");
let authMode = "login";

async function authAPI(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function configureAuthForm(configured, setupAllowed) {
  if (!configured && !setupAllowed) {
    authTitle.textContent = "等待本机设置管理员账号";
    authSubtitle.textContent = "请先在 Mac 本机打开 DJSMSForward";
    authForm.hidden = true;
    return;
  }
  authMode = configured ? "login" : "setup";
  authTitle.textContent = configured ? "登录 DJSMSForward" : "设置管理员账号";
  authSubtitle.textContent = configured ? "本机管理后台" : "首次启动";
  authConfirmRow.hidden = configured;
  authConfirm.required = !configured;
  authUsername.autocomplete = "username";
  authPassword.autocomplete = configured ? "current-password" : "new-password";
  authConfirm.autocomplete = "new-password";
  authSubmit.textContent = configured ? "登录" : "创建并登录";
  authForm.hidden = false;
  authUsername.focus();
}

async function loadAuthStatus() {
  try {
    const status = await authAPI("/api/auth/status");
    if (status.authenticated) {
      window.location.replace("/");
      return;
    }
    configureAuthForm(Boolean(status.configured), Boolean(status.setup_allowed));
  } catch (error) {
    authTitle.textContent = "账号服务不可用";
    authSubtitle.textContent = "请检查 DJSMSForward 日志";
    authError.textContent = error.message;
  }
}

authForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  authError.textContent = "";
  if (authMode === "setup" && authPassword.value !== authConfirm.value) {
    authError.textContent = "两次输入的密码不一致";
    return;
  }
  authSubmit.disabled = true;
  try {
    const credentials = {
      username: authUsername.value.trim(),
      password: authPassword.value,
    };
    if (authMode === "setup") {
      await authAPI("/api/auth/setup", {
        method: "POST",
        body: JSON.stringify(credentials),
      });
    }
    await authAPI("/api/auth/login", {
      method: "POST",
      body: JSON.stringify(credentials),
    });
    window.location.replace("/");
  } catch (error) {
    authError.textContent = error.message;
    authPassword.value = "";
    authConfirm.value = "";
    authPassword.focus();
  } finally {
    authSubmit.disabled = false;
  }
});

void loadAuthStatus();
