// 主题保存在软件独立的本机偏好中，切换时不修改任何版本记录。
(() => {
  const STORAGE_KEY = "release-desk-theme";
  const DEFAULT_THEME = "night";
  const THEMES = {
    night: "深灰蓝",
    graphite: "石墨灰",
    purple: "暗夜紫",
    forest: "森林绿",
    light: "柔和浅色",
  };

  /**
   * 应用已知主题并记录本机偏好，未知名称回退到低亮度默认主题。
   * @param {string} name 内置主题的稳定标识，不接受任意 CSS 或颜色代码。
   * @returns {void} 只更新页面主题；本地存储不可用时仍正常显示。
   */
  function applyTheme(name) {
    // 白名单控制样式入口，过期或被修改的偏好不会造成空白页面。
    const theme = Object.hasOwn(THEMES, name) ? name : DEFAULT_THEME;
    document.documentElement.dataset.theme = theme;
    // 隐私模式或系统策略可能禁止存储，显示主题不应因此失败。
    try {
      localStorage.setItem(STORAGE_KEY, theme);
    } catch {
      /* 仅本次生效。 */
    }
  }

  // 在首屏样式加载前应用主题，避免深色用户先看到大面积白色闪屏。
  let savedTheme = document.documentElement.dataset.theme || DEFAULT_THEME;
  applyTheme(savedTheme);
  document.addEventListener("DOMContentLoaded", () => {
    const select = document.querySelector("#theme-select");
    select.innerHTML = Object.entries(THEMES)
      .map(([key, name]) => `<option value="${key}">${name}</option>`)
      .join("");
    select.value = document.documentElement.dataset.theme;
    select.addEventListener("change", async () => {
      const previous = document.documentElement.dataset.theme;
      applyTheme(select.value);
      select.disabled = true;
      // 端口每次启动可能变化，因此持久化到软件数据目录，不能只依赖 localStorage。
      try {
        const response = await fetch("/api/theme/save", {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-Release-Token": document.querySelector(
              'meta[name="release-token"]',
            ).content,
          },
          body: JSON.stringify({ theme: select.value }),
        });
        const result = await response.json();
        if (!response.ok || result.error)
          throw new Error(result.error || "主题保存失败");
      } catch (error) {
        applyTheme(previous);
        select.value = previous;
        document.dispatchEvent(
          new CustomEvent("theme-error", { detail: error.message }),
        );
      } finally {
        select.disabled = false;
      }
    });
  });
})();
