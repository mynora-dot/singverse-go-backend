# 登录偏好与密码重置

## 记住密码

网页仅在本机记录账号、是否使用浏览器记住密码、是否自动登录三个偏好，不将密码、MFA 动态码、重置码或会话令牌写入 localStorage/sessionStorage。选择“记住密码”后，完整认证成功才调用浏览器密码管理接口；支持时从浏览器保存的凭据填充表单。普通 HTTP 局域网入口或不支持该接口的浏览器，使用浏览器自身的密码管理/自动填充功能，并明确提示由浏览器保存，不宣称应用已保存。是否保存及自动填充最终由浏览器和用户确认。

## 自动登录

`POST /api/v1/auth/login` 增加可选布尔字段 `autoLogin`，默认 false，旧客户端无需修改。

- false/省略：原有会话规则，空闲 30 分钟或绝对 8 小时过期；Cookie 不设置持久有效期。
- true：验证成功后的 `singverse_session` Cookie 使用 `Max-Age=2592000` 与对应 Expires，数据库令牌绝对 30 天过期，不延长此期限。该选项明确覆盖普通会话的空闲 30 分钟限制。
- 开启 MFA 时，选项写入 5 分钟挑战的服务端状态；挑战 Cookie 不持久化，也不授予管理身份。完成 MFA 时沿用该选项，不接受在验证接口临时升级会话期限。
- 保持 HttpOnly、SameSite=Lax、Path=/、无 Domain；HTTPS 使用 Secure。同源与 CSRF 边界保持有效。
- 自动登录偏好仅控制首页跳转，实际权限仍由 `/api/v1/auth/session` 校验服务端 Cookie。退出登录撤销当前令牌、清除 Cookie，并关闭当前浏览器自动登录偏好；改密、改账号、MFA 安全变更和管理员恢复继续撤销所有会话。

## 忘记密码

`POST /api/v1/auth/password/reset` 允许未登录用户提交：

```json
{"username":"当前管理员账号","resetToken":"部署主机生成的43位一次性重置码","newPassword":"15～128个Unicode字符的新密码"}
```

请求仍须先获取 `/api/v1/auth/csrf` 并发送 `X-CSRF-Token`，遵循同源和账号/来源地址限流。账号或重置码不匹配、过期、已使用返回统一 401；密码不符合现有规则返回 400；被限流返回 429 和 Retry-After。成功返回 200 空响应体，清除浏览器认证 Cookie，不自动登录。

重置码只能由部署主机的 `singverse --issue-password-reset` 命令生成。该命令与 HTTP 服务使用相同部署锁和数据库配置，须先停止业务进程，要求真实交互 TTY 和输入 RESET。命令输出 32 随机字节的无填充 Base64URL（43 位），仅存 SHA-256 摘要，15 分钟有效；再次生成立即撤销旧重置码。没有生成重置码的 HTTP 接口，不使用 MFA 恢复码代替密码重置码。

成功重置只更新密码和默认密码标志，保留账号、MFA 秘钥与剩余 MFA 恢复码；同一事务内提高账号 revision 并撤销所有会话、挑战和重置码。随后用新密码和已配置的 MFA 重新登录。若还丢失 MFA，保留原有仅部署主机可执行的 `--reset-admin` 紧急恢复流程。认证日志只记录事件类型与时间，不记录任何密码或重置码。

新增 SQLite/MySQL 迁移 `202611120001_login_preferences.sql`，为 `auth_token` 添加默认 0 的 `persistent` 标志，保留原有令牌与业务数据。接口定义见 `openapi/singverse.openapi.json`。
