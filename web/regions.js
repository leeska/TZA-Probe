// 内置目录只覆盖国内三网，节点和自定义目标却常放在香港、美国等海外地区。
// 建议列表里补一份常用海外地区：不影响自由填写，只是让下拉里能直接选到。
export const overseasRegions = [
  "香港",
  "澳门",
  "台湾",
  "日本",
  "韩国",
  "新加坡",
  "马来西亚",
  "泰国",
  "越南",
  "菲律宾",
  "印度尼西亚",
  "印度",
  "阿联酋",
  "以色列",
  "土耳其",
  "澳大利亚",
  "新西兰",
  "美国",
  "加拿大",
  "墨西哥",
  "巴西",
  "英国",
  "爱尔兰",
  "法国",
  "德国",
  "荷兰",
  "比利时",
  "瑞士",
  "奥地利",
  "瑞典",
  "挪威",
  "丹麦",
  "芬兰",
  "意大利",
  "西班牙",
  "葡萄牙",
  "波兰",
  "捷克",
  "俄罗斯",
  "南非",
];

// 目录地区、节点历史上用过的值、常用海外地区合并成一份建议列表，按拼音排序。
export function regionOptions(...groups) {
  const merged = new Set(overseasRegions);
  for (const group of groups) {
    for (const value of group || []) {
      const region = String(value ?? "").trim();
      if (region) merged.add(region);
    }
  }
  return [...merged].sort((a, b) => a.localeCompare(b, "zh-Hans-CN"));
}
