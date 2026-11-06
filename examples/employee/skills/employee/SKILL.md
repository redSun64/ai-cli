---
name: employee
description: 员工账户开通、账户状态核验和已有账户角色补救。用户要求创建员工账户、查询账户状态或补绑角色时使用。
---

## 业务流程

先通过 schema 查找 employee 命令组及相应叶子命令。

- 开通新账户：选择 +onboard。流程查找员工、创建账户、绑定默认角色；完成标志是三步均成功。
- 查询或核验：选择 +status，使用已有 account-id；该命令只读。
- 已有账户角色补救：选择 +grant-role，预演 account-id 和 role 后按 SDK 协议取得授权。

遇到部分成功、超时或效果未知时，读取 [恢复说明](references/recovery.md)。
