-- accounts 表新增邮箱验证状态字段。
-- DEFAULT 1 让已有账号沿用"已验证"状态——旧注册流程在注册前已校验邮箱验证码。

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS email_verified TINYINT(1) NOT NULL DEFAULT 1;
