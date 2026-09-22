// 生成 web/pinyin.js：搜索要用的汉字 → 无调拼音。
// pinyin-pro 只在生成时用得到，运行时页面不带任何依赖。装到任意目录后指向它即可：
//   cd /tmp && npm i pinyin-pro
//   PINYIN_PRO=/tmp/node_modules/pinyin-pro/dist/esm/index.mjs node <仓库>/scripts/gen-pinyin.mjs
import { writeFile } from "node:fs/promises";
import { overseasRegions } from "../web/regions.js";

const { pinyin } = await import(process.env.PINYIN_PRO || "pinyin-pro");

// 目录里的地区（三网目录只覆盖这些省/直辖市/自治区）。
const catalogRegions = "上海 云南 内蒙古 北京 吉林 四川 天津 宁夏 安徽 山东 山西 广东 广西 新疆 江苏 江西 河北 河南 浙江 海南 湖北 湖南 甘肃 福建 西藏 贵州 辽宁 重庆 陕西 青海 黑龙江".split(" ");
// 节点常落在这些城市，地区字段可以填城市名。
const cities = "成都 深圳 杭州 广州 南京 苏州 武汉 西安 青岛 大连 沈阳 哈尔滨 长春 石家庄 太原 呼和浩特 济南 郑州 合肥 南昌 福州 厦门 长沙 南宁 海口 贵阳 昆明 拉萨 兰州 西宁 银川 乌鲁木齐 无锡 宁波 温州 佛山 东莞 珠海 中山 泉州 常州 徐州 烟台 潍坊 洛阳 唐山 保定 廊坊 邯郸 大同 包头 鞍山 抚顺 吉林 齐齐哈尔 大庆 淄博 临沂 济宁 泰安 威海 日照 蚌埠 芜湖 九江 赣州 汕头 惠州 江门 湛江 桂林 柳州 三亚 洛阳 襄阳 宜昌 岳阳 株洲 衡阳 南阳 新乡 焦作 许昌 潮州 揭阳 梅州 河源 清远 韶关 肇庆 茂名 阳江 云浮 汕尾 台北 高雄 台中 台南 新竹 基隆 嘉义".split(" ");
// 节点/任务名里常见的词，顺手收进来。
const words = "机房 服务器 节点 主机 家庭 宽带 家宽 中转 直连 回程 延迟 联通 电信 移动 其他 测试 香港 台湾 澳门 日本 韩国 新加坡 美国 英国 德国 法国 荷兰 俄罗斯 加拿大 澳大利亚 印度 巴西 越南 泰国 菲律宾 马来西亚 印度尼西亚 新西兰 瑞士 瑞典 挪威 丹麦 芬兰 波兰 捷克 西班牙 葡萄牙 意大利 爱尔兰 奥地利 比利时 墨西哥 阿根廷 南非 阿联酋 以色列 土耳其 乌克兰 沙特 埃及 希腊 匈牙利 罗马尼亚 智利 秘鲁 哥伦比亚 巴基斯坦 孟加拉 斯里兰卡 尼泊尔 蒙古 哈萨克斯坦 乌兹别克斯坦 柬埔寨 缅甸 老挝 文莱 卡塔尔 科威特 巴林 阿曼 约旦 黎巴嫩".split(" ");

const vocabulary = [...catalogRegions, ...overseasRegions, ...cities, ...words];
const chars = [...new Set(vocabulary.join("").split(""))]
  .filter((c) => /[\u4e00-\u9fa5]/.test(c))
  .sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));

const rows = chars.map((c) => {
  const [syllable] = pinyin(c, { toneType: "none", type: "array" });
  return `  ${JSON.stringify(c)}: ${JSON.stringify(syllable ?? "")},`;
});
// 多音字地名（重庆 chongqing、厦门 xiamen、蚌埠 bengbu…）逐字取音会读错，所以另外导出按词定音的表。
const phrases = [...new Set(vocabulary)]
  .filter((term) => [...term].length > 1)
  .sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
const phraseRows = phrases.map((term) => {
  const syllables = pinyin(term, { toneType: "none", type: "array" });
  return `  ${JSON.stringify(term)}: ${JSON.stringify(syllables)},`;
});
const out = `// 本文件由 scripts/gen-pinyin.mjs 生成，请勿手改：改词表后重跑脚本。
// 只收录搜索会用到的字（目录地区、常用城市、海外地区、常见词）。表外的汉字退回纯文本匹配，
// 英文与代号见 web/search.js 的别名表。
export const pinyinMap = {
${rows.join("\n")}
};

// 按词定音：多音字在词里的读法（重庆 = chong qing，不是 zhong qing）。
export const pinyinPhrases = {
${phraseRows.join("\n")}
};
`;
await writeFile(new URL("../web/pinyin.js", import.meta.url), out);
const blank = rows.filter((r) => r.endsWith('"",')).length;
console.log(`已写入 web/pinyin.js：${chars.length} 个字、${phrases.length} 个词（缺拼音 ${blank} 个）`);
