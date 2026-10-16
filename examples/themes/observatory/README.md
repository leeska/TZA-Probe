# 观测台：完整独立前台示例

此主题的 HTML、侧栏、数据表、搜索排序、详情路由和图表均由本目录实现，不使用核心 DOM、CSS 或 UI 模块。
`main.js` 通过 `/theme-sdk.js` 读公开数据及宿主路由；`format.mjs` 是静态模块，`chart.mjs` 是动态加载的独立图表模块，`labels.json` 是包内 fetch 的静态配置。

```sh
go run ./tools/themepack -out dist/themes examples/themes/observatory
```

上传生成的 zip 后，在前台选「观测台」，或由后台设置为默认外观。直接打开 `/?tza-theme=observatory` 可以预览。
前台损坏时 `/?tza-theme=shadcn` 恢复；管理入口一直是 `/admin`。

接口和打包约定见 [独立前台开发指南](../../../docs/FRONTENDS.md)。README 不进入安装包。
