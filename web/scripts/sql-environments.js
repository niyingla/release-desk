// 选择只影响当前 SQL 视图及后续预览；正在执行的任务始终绑定后端确认环境。
let selectedSQLEnvironment = "test";

/**
 * 取得环境显示名称，未知历史标识原样显示。
 * @param {string} environment 环境标识；旧历史空值属于测试。
 * @returns {string} 由后端元数据提供的名称，不据此改变实际连接。
 */
function sqlEnvironmentLabel(environment = selectedSQLEnvironment) {
  const id = environment || "test";
  return (
    state.sql_environments?.find((option) => option.id === id)?.label || id
  );
}

/**
 * 生成 SQL 表格中的环境选择框。
 * @returns {string} 下拉控件；切换本身不连接数据库、不执行 SQL。
 */
function sqlEnvironmentControl() {
  return `<label class="sql-environment-control">执行环境 <select id="sql-environment" title="切换 SQL 预览与执行环境；执行状态按环境独立显示">${(state.sql_environments || []).map((option) => `<option value="${escapeHTML(option.id)}" ${option.id === selectedSQLEnvironment ? "selected" : ""}>${escapeHTML(option.label)}</option>`).join("")}</select></label>`;
}

/**
 * 将所有环境的独立连接摘要生成配置字段，密码从不回填。
 * @param {Array<object>} profiles 无密码的环境连接摘要，按环境标识定位字段。
 * @returns {string} 各自独立的主机、端口、账号和空密码输入。
 */
function sqlConnectionFields(profiles) {
  return profiles
    .map((db) => {
      const prefix = `db_${db.environment}_`;
      // 可留空未使用环境；填写 UAT 不会要求重新配置已有测试连接。
      return `<div class="section-label">${escapeHTML(sqlEnvironmentLabel(db.environment))} MySQL</div><div class="form-grid">${formField("主机", prefix + "host", db.host)}${formField("端口", prefix + "port", db.port, { type: "number" })}${formField("业务账号", prefix + "user", db.user)}${formField("密码", prefix + "password", "", { type: "password", placeholder: db.configured ? "留空保留本环境密码" : "填写此环境数据库密码" })}</div>`;
    })
    .join("");
}

/**
 * 保存已经完整填写的环境配置，未配置环境保持空白。
 * @param {object} data 当前设置表单的字符串字段，不写入版本记录或日志。
 * @returns {Promise<void>} 校验全部输入后逐环境保存；服务端错误保留在当前表单提示。
 */
async function saveSQLConnections(data) {
  const profiles = (state.sql_environments || [])
    .map(({ id }) => {
      const prefix = `db_${id}_`;
      return {
        environment: id,
        host: data[prefix + "host"].trim(),
        port: Number(data[prefix + "port"]),
        user: data[prefix + "user"].trim(),
        password: data[prefix + "password"],
      };
    })
    .filter((profile) => profile.host || profile.user || profile.password);
  // 先校验全部表单，再开始保存，减少填写不完整造成的部分更新。
  for (const profile of profiles) {
    if (
      !profile.host ||
      !profile.user ||
      !Number.isInteger(profile.port) ||
      profile.port < 1 ||
      profile.port > 65535
    )
      throw new Error(
        `请完整填写${sqlEnvironmentLabel(profile.environment)}连接信息`,
      );
  }
  for (const profile of profiles) await api("db/save", profile);
}
