# 插件管理

后台「插件」是唯一入口。声明式通知与目标集插件使用 `plugin.json`，由 Probe 解释执行；
安装前确认能力与外部域名。格式见 [插件说明](PLUGINS.md)。

地区偏移检测（Google / Meta 页面地区）不是插件：它是 Server 内置功能，浏览器随
`tza-server` 一起发布，用安装脚本的 `--with-browser` 启用，见 [地区偏移检测](IP-GUARD-INTEGRATION.md)。
包 ID `ipsentinel-runtime` 保留不用，上传的插件不能占用它。
