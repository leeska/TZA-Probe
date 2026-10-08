// 二次元皮肤的角色名单：面板节点名 → 立绘。
//
// 约定的设定：**形象 = 线路身份 × 商家气质**。只对线路机做形象设计，名单外的节点在
// 二次元皮肤里照旧是一张浅色卡片（没有人物），不写占位立绘。
// 图片放 web/anime/<id>.webp（go:embed 进二进制），样式在 web/anime.css 里按 [data-cast] 挂。
//
// 键是节点名的小写形态——面板里的名字是 JP-NoSla-Gen2，用户定的写法不能改，所以这里只做
// 大小写归一，不重写名字。
export const CAST = {
  "jp-nosla-gen2": {
    id: "jp-nosla-gen2",
    line: "Gen.2",
    vendor: "NoSla",
    crew: "家人云",
    note: "亚太顶尖精品线 → 小天使待遇；NoSla 家人云 → 嘴硬心软的傲娇妹妹，茶给你倒好",
  },
};

// 有立绘的节点才画人物：名字（大小写不敏感、去首尾空格）在名单里即可。
export function castOf(node) {
  const key = String(node?.name || "").trim().toLowerCase();
  return CAST[key] || null;
}

export function castId(node) {
  return castOf(node)?.id || "";
}
