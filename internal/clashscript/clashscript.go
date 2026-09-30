/**
 * Clash 分流脚本模板：放进 Clash Verge 的「全局扩展脚本」，受保护的程序与网站走住宅出口，其他走基础节点。
 * 守护引擎依赖这份脚本建立的「住宅出口」分组与按程序分流规则。
 */
package clashscript

import _ "embed"

/** Template：脚本模板，用户只需填写开头的住宅代理 */
//
//go:embed clash_script.js
var Template string
