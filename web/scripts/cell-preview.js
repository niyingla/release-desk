// 表格全文预览独立于业务页面，自动覆盖所有可双击复制的单元格。
(() => {
  const VIEWPORT_MARGIN = 12;
  const HIDE_DELAY_MS = 140;
  let preview;
  let hideTimer;
  let anchorCell;
  let anchorBounds;

  /**
   * 关闭全文预览并取消延迟关闭，避免切页或滚动后留下旧内容。
   * @returns {void} 仅修改浮层可见性，不改变单元格内容或复制状态。
   */
  function hidePreview() {
    // 页面滚动、窗口缩放和退出悬停共用同一关闭入口。
    clearTimeout(hideTimer);
    if (preview) preview.hidden = true;
  }

  /**
   * 在单元格附近展示完整原文，长内容可在浮层内滚动。
   * @param {HTMLElement} cell 带 data-copy 的表格单元格，属性中保存未截断的原文。
   * @returns {void} 只展示文字；空内容关闭浮层，不触发编辑、保存或执行。
   */
  function showPreview(cell) {
    // 弹窗打开时，背景表格不能再弹出浮层遮挡确认或关闭按钮。
    const modal = document.querySelector(".modal-overlay");
    if (modal && !modal.contains(cell)) return hidePreview();
    // 默认读取复制原文；Cron 可提供完整派生解释，双击仍只复制表达式。
    const text = cell.dataset.preview || cell.dataset.copy;
    if (!text) return hidePreview();
    clearTimeout(hideTimer);

    // 浮层挂在 body，避免被表格的 overflow 或弹窗容器裁剪。
    // 全页面复用一个节点，不在每次进入单元格时重复创建。
    if (!preview) {
      preview = document.createElement("div");
      preview.className = "cell-preview";
      preview.setAttribute("role", "tooltip");
      preview.innerHTML =
        '<div class="cell-preview-heading">完整内容<span>可滚动查看 · 双击单元格复制</span></div><div class="cell-preview-content"></div>';
      document.body.appendChild(preview);
    }
    // 用户填写的配置和 SQL 必须作为纯文本，HTML 标签也原样展示。
    preview.querySelector(".cell-preview-heading").firstChild.textContent =
      cell.dataset.previewTitle || "完整内容";
    preview.querySelector(".cell-preview-content").textContent = text;
    preview.hidden = false;
    preview.querySelector(".cell-preview-content").scrollTop = 0;

    // 先测量实际高度，再选择下方或上方，始终留在可见窗口内。
    // 贴近单元格边缘，鼠标可直接进入浮层滚动查看长文本。
    const cellRect = cell.getBoundingClientRect();
    // 保存显示时的位置；滚动到单元格的事件可能晚于 pointerover 到达。
    anchorCell = cell;
    anchorBounds = cellRect;
    const panelRect = preview.getBoundingClientRect();
    const top =
      cellRect.bottom + panelRect.height <= window.innerHeight - VIEWPORT_MARGIN
        ? cellRect.bottom - 1
        : cellRect.top - panelRect.height + 1;
    preview.style.left = `${Math.max(VIEWPORT_MARGIN, Math.min(cellRect.left, window.innerWidth - panelRect.width - VIEWPORT_MARGIN))}px`;
    preview.style.top = `${Math.max(VIEWPORT_MARGIN, Math.min(top, window.innerHeight - panelRect.height - VIEWPORT_MARGIN))}px`;
  }

  /**
   * 判断当前锚点是否真正离开显示时的位置。
   * @returns {boolean} 锚点被重绘或移动时为 true；迟到但已体现在布局中的滚动不算移动。
   */
  function anchorMoved() {
    if (!anchorCell?.isConnected || !anchorBounds) return true;
    const rect = anchorCell.getBoundingClientRect();
    return ["top", "left", "bottom", "right"].some(
      (edge) => Math.abs(rect[edge] - anchorBounds[edge]) > 0.5,
    );
  }

  document.addEventListener("pointerover", (event) => {
    // 允许鼠标停在浮层里滚动或选择文字，进入内容区不关闭预览。
    if (preview?.contains(event.target)) return clearTimeout(hideTimer);
    const cell = event.target.closest("[data-copy]");
    if (cell) showPreview(cell);
  });
  document.addEventListener("pointerout", (event) => {
    // 单元格内部元素之间移动无需关闭；跨越到浮层时保留当前内容。
    const next = event.relatedTarget;
    if (
      next instanceof Element &&
      (next.closest("[data-copy]") || preview?.contains(next))
    )
      return;
    hideTimer = setTimeout(hidePreview, HIDE_DELAY_MS);
  });
  document.addEventListener(
    "scroll",
    (event) => {
      // 浮层内滚动保持可见；页面滚动仅在实际移动锚点时关闭。
      // 忽略已经体现在 showPreview 布局中的迟到事件，避免刚进入浮层就误关闭。
      if (!preview?.contains(event.target) && anchorMoved()) hidePreview();
    },
    true,
  );
  window.addEventListener("resize", hidePreview);
  // 弹窗切换会替换悬停锚点，旧全文预览必须立即清理。
  document.addEventListener("release-desk:modal", hidePreview);
  document.addEventListener("keydown", (event) => {
    // Escape 只退出查看，原有页面的弹窗关闭逻辑仍正常工作。
    if (event.key === "Escape") hidePreview();
  });
})();
