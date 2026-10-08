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
  "de-nosla-fra": {
    id: "de-nosla-fra",
    line: "三网回程 CTG · 10099→9929 · CMIN2",
    vendor: "NoSla",
    crew: "家人云",
    note: "法兰克福，欧洲线里回程最规整的一条（150–190ms）；德国元素落在黑红金三色缎带、Dirndl 围裙与麻花辫上的椒盐卷饼发夹",
  },
  "us-vmiss-tri": {
    id: "us-vmiss-tri",
    line: "CN2GIA · 10099→9929 · CMIN2",
    vendor: "VMISS",
    crew: "云上飞行",
    note: "美西三网精品（CN2GIA / 9929 / CMIN2，160ms 上下）；卖家 VMISS 是加拿大公司，官网是蓝白科技风 + 云朵涂鸦 + 亮黄点缀，所以人物做成蓝挑染 + 云朵发夹 + 星条缎带 + 黄星发夹，胸口铭牌写 VMISS",
  },
  "us-bwh-dc9": {
    id: "us-bwh-dc9",
    line: "CN2GIA · 10099→9929 · CMIN2",
    vendor: "BandwagonHost",
    crew: "搬瓦工",
    note: "洛杉矶 DC9：电信 CN2GIA + 联通 9929 + 移动 CMIN2（159–167ms）；卖家是搬瓦工（BandwagonHost），官网是金黄→琥珀渐变 #FFC107→#F57C00 配深棕/黑与银灰 HOST，吉祥物意象是黑机架 + 金色灯；所以人物做成蜜金发 + 银灰挑染 + 金领巾，配一枚大篷车轮发夹，再叠美国星条缎带，胸口铭牌写 Bandwagon",
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
