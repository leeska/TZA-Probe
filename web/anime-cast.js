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
    note: "Gen.2 亚太精品线 × 家人云；三网回程 CTG / 10099→9929 / CMIN2，61/57/65ms。日本元素 = 樱花簪 + 千鸟格水手领 + 红白缎带；人物 = 银白拖到腰的长直发 + 猫耳 + 紫瞳半眯的傲娇脸（全系列唯一的兽耳、唯一的紫瞳），藏青水手服，胸口「家人」铭牌",
  },
  "de-nosla-fra": {
    id: "de-nosla-fra",
    line: "三网回程 CTG · 10099→9929 · CMIN2",
    vendor: "NoSla",
    crew: "家人云",
    note: "法兰克福，欧洲线里回程最规整的一条（150–190ms）。德国元素 = 黑红金缎带 + Dirndl（白衬衫 / 深绿系带胸衣 / 白围裙）+ 麻花辫上的椒盐卷饼发夹；人物 = 蜂蜜金发盘成麻花冠 + 灰绿瞳的端庄微笑（唯一的盘发、唯一传统服饰），胸口「家人」铭牌",
  },
  "us-vmiss-tri": {
    id: "us-vmiss-tri",
    line: "CN2GIA · 10099→9929 · CMIN2",
    vendor: "VMISS",
    crew: "云上飞行",
    note: "美西三网精品（CN2GIA / 9929 / CMIN2，160ms 上下）。卖家 VMISS 官网是蓝白科技风 + 云朵涂鸦 + 亮黄点缀，人物据此做：栗棕双马尾 + 电蓝挑染 + 圆亮蓝瞳 + 张口大笑挥手（唯一的双马尾、唯一连帽衫、唯一大幅笑容），白色 techwear 连帽衫 + 云朵发夹 + 黄星发夹 + 星条缎带，胸口铭牌写 VMISS",
  },
  "us-bwh-dc9": {
    id: "us-bwh-dc9",
    line: "CN2GIA · 10099→9929 · CMIN2",
    vendor: "BandwagonHost",
    crew: "搬瓦工",
    note: "洛杉矶 DC9：CN2GIA + 9929 + CMIN2（159–167ms）。卖家搬瓦工（BandwagonHost）官网是金黄→琥珀渐变 #FFC107→#F57C00 配深棕/黑与银灰 HOST、黑机架 + 金色灯；人物 = 琥珀棕高马尾 + 银灰挑染 + 金瞳 + 咧嘴露虎牙（唯一的高马尾、唯一工装背带裤、唯一趴在牌上的姿势），深棕背带裤 + 大篷车轮发夹 + 星条缎带 + 金星别针，胸口铭牌写 Bandwagon",
  },
  // 通用人物：线路身份 × 商家气质都定不出形象的节点（名单以外）统一落到这一张中性立绘，
  // 于是二次元皮肤下所有机器都有人物、观感统一。中性设计、不带任何国家/商家元素，
  // 画布 / 缩放 / 手位与那四台同一套约定（共用同一份 CSS 几何变量）。由 castId() 兜底。
  "generic": {
    id: "generic",
    line: "",
    vendor: "",
    crew: "",
    note: "通用默认人物：深棕长直发 + 白衬衫 + 藏青百褶裙，中性日常、无任何国家 / 商家元素；整身立绘 + 手蒙版 + 闭眼帧同一套约定。",
  },
};

// 有立绘的节点才画人物：名字（大小写不敏感、去首尾空格）在名单里即可。
export function castOf(node) {
  const key = String(node?.name || "").trim().toLowerCase();
  return CAST[key] || null;
}

// 画哪张立绘：名单里的用自己的，名单以外的统一落到通用人物 "generic"。
// 名字里有独立形象的四台仍返回自己的 id，别处的判断（折叠键、牌面配色）都不受影响。
export function castId(node) {
  return castOf(node)?.id || "generic";
}

// 折叠状态用哪把钥匙：四台专属立绘仍用自己的 id（沿用既有 localStorage 记录），
// 通用人物的多张卡各用各的节点 id —— 否则点一张会连带收起其它所有通用卡。
export function foldKey(node) {
  const c = castOf(node);
  return c ? c.id : "node-id:" + (node?.id ?? "");
}
