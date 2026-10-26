// 与后端目标库规则一致：模板优先，同前缀数字尾号自然升序，不修改执行或历史对象。
/**
 * 比较库名尾号，保留超大数字和前导零的确定性。
 * @param {string} left 左侧完整数据库名。
 * @param {string} right 右侧完整数据库名。
 * @returns {number} 负值表示左侧排前，不按整数转换避免精度丢失。
 */
function compareDatabaseNames(left, right) {
  const parts = (name) => {
    const match = String(name).match(/^(.*?)([0-9]+)$/);
    return match
      ? [match[1], match[2].replace(/^0+(?=[0-9])/, "")]
      : [String(name), ""];
  };
  const [lp, ln] = parts(left),
    [rp, rn] = parts(right);
  const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
  if (lp !== rp) return compare(lp, rp);
  // 模板无尾号，比数字零还早；超大编号按长度再比较数字文本。
  if (!ln || !rn) return compare(ln, rn);
  if (ln.length !== rn.length) return ln.length - rn.length;
  return compare(ln, rn) || compare(left, right);
}

/**
 * 返回按库名排列的展示副本，同库多次记录保留原有时间顺序。
 * @param {Array<object>} rows 后台状态或历史记录，不可原地重排。
 * @returns {Array<object>} 与执行预览一致的自然数字顺序。
 */
function orderedDatabaseRows(rows) {
  return [...rows].sort((a, b) => compareDatabaseNames(a.database, b.database));
}
