// 选择只影响当前 SQL 视图及后续预览；正在执行的任务始终绑定后端确认环境。
let selectedSQLEnvironment = "test";

/**
 * 取得环境显示名称，未知历史标识原样显示。
 * @param {string} environment 环境标识；旧历史空值属于测试。
 * @returns {string} 由后端元数据提供的名称，不据此改变实际连接。
 */
function sqlEnvironmentLabel(environment = selectedSQLEnvironment) {
  // 旧执行历史未保存环境时按兼容约定显示测试，不修改原记录。
  // 显示名称来自后端元数据；未知非空标识保留原文，不能猜成线上。
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
  // 下拉选项只展示后端允许的执行环境，切换本身不会建立数据库连接。
  return `<label class="sql-environment-control">执行环境 <select id="sql-environment" title="切换 SQL 预览与执行环境；执行状态按环境独立显示">${(state.sql_environments || []).map((option) => `<option value="${escapeHTML(option.id)}" ${option.id === selectedSQLEnvironment ? "selected" : ""}>${escapeHTML(option.label)}</option>`).join("")}</select></label>`;
}

/**
 * 将所有环境的独立连接摘要生成配置字段，密码从不回填。
 * @param {Array<object>} profiles 无密码的环境连接摘要，按环境标识定位字段。
 * @returns {string} 各自独立的主机、端口、账号和空密码输入。
 */
function sqlConnectionFields(profiles) {
  // 每个环境使用独立字段前缀，测试与 UAT 的凭据不能在提交时相互覆盖。
  // 摘要不包含解密密码，已有连接通过占位说明提示留空保留。
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
  // 先从当前允许的环境集合读取字段，不接收任意环境名称。
  // 主机与账号去掉首尾空白，密码保留原值以免改变凭据。
  // 空密码具有保留已有密码的语义，不能主动用空字符串覆盖连接。
  // 全空且未使用的环境不参与此次保存。
  // 端口必须是合法整数，避免界面字符串被隐式转换成另一连接。
  // 只校验输入是否完整，不在表单处理时执行 SQL。
  // 全部配置先检查，减少第一项保存后才发现第二项缺字段的情况。
  // 保存按环境逐项调用，服务端仍需独立加密和核验环境归属。
  // 保存失败会向设置窗口抛出错误，不伪造全部环境更新成功。
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
