// 进度轮询保留现有节点，只更新变化的文本与属性，避免滚动和动画反复重置。
/**
 * 将已转义的进度模板同步到现有容器。
 * @param {Element} container 当前进度或按钮容器。
 * @param {string} html 由本应用生成、外部文本已转义的 HTML。
 * @returns {void} 保留未变节点、焦点和各层滚动位置。
 */
function updateProgressView(container, html) {
  const template = document.createElement("template");
  template.innerHTML = html;
  // 不整体替换面板，已有展开状态由各面板的模板负责恢复。
  syncProgressChildren(container, template.content);
}

/**
 * 同步一层节点，以稳定键识别新增进展和队列行。
 * @param {Node} current 正在展示的父节点。
 * @param {Node} incoming 新模板中的父节点。
 * @returns {void} 同一记录继续复用原 DOM；过期节点才移除。
 */
function syncProgressChildren(current, incoming) {
  let cursor = current.firstChild;
  for (const next of [...incoming.childNodes]) {
    const key =
      next.nodeType === 1 ? next.getAttribute("data-progress-key") : null;
    // 新摘要插入顶部时移动对应节点，不覆盖正在阅读的旧摘要。
    let node = key
      ? [...current.childNodes].find(
          (child) =>
            child.nodeType === 1 &&
            child.getAttribute("data-progress-key") === key,
        )
      : cursor;
    if (
      !node ||
      node.nodeType !== next.nodeType ||
      node.nodeName !== next.nodeName ||
      (!key && node.nodeType === 1 && node.hasAttribute("data-progress-key"))
    ) {
      node = next.cloneNode(true);
      current.insertBefore(node, cursor);
    } else {
      if (node !== cursor) current.insertBefore(node, cursor);
      if (node.nodeType === 1) {
        // 仅改动确实变化的属性，避免动画、焦点和详情展开被重置。
        for (const attr of [...node.attributes])
          if (!next.hasAttribute(attr.name)) node.removeAttribute(attr.name);
        for (const attr of next.attributes)
          if (node.getAttribute(attr.name) !== attr.value)
            node.setAttribute(attr.name, attr.value);
        syncProgressChildren(node, next);
      } else if (node.nodeValue !== next.nodeValue)
        node.nodeValue = next.nodeValue;
    }
    cursor = node.nextSibling;
  }
  // 有限进展列表淘汰旧记录时才清理尾部，不改变保留条目的身份。
  while (cursor) {
    const obsolete = cursor;
    cursor = cursor.nextSibling;
    obsolete.remove();
  }
}
