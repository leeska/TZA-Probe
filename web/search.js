// 目录、节点、任务等处的筛选统一走这里：中文、全拼、拼音首字母、英文与代号都能命中。
// 例：香港 → 香港 / xianggang / xg / hk / hongkong；电信 → 电信 / dianxin / dx / ct / telecom。
import { pinyinMap, pinyinPhrases } from "./pinyin.js";

// 代号与英文名。全拼与首字母由 pinyin.js 自动得出，这里只补代号、英文写法和常见城市。
const aliases = {
  香港: ["hk", "hongkong", "hkg"],
  澳门: ["mo", "macau", "macao"],
  台湾: ["tw", "taiwan", "taipei"],
  日本: ["jp", "japan", "tokyo", "osaka"],
  韩国: ["kr", "korea", "seoul"],
  新加坡: ["sg", "singapore"],
  马来西亚: ["my", "malaysia", "kuala lumpur"],
  泰国: ["th", "thailand", "bangkok"],
  越南: ["vn", "vietnam"],
  菲律宾: ["ph", "philippines", "manila"],
  印度尼西亚: ["id", "indonesia", "jakarta"],
  印度: ["in", "india", "mumbai"],
  阿联酋: ["ae", "uae", "emirates", "dubai"],
  以色列: ["il", "israel"],
  土耳其: ["tr", "turkey", "istanbul"],
  澳大利亚: ["au", "australia", "sydney"],
  新西兰: ["nz", "new zealand", "auckland"],
  美国: ["us", "usa", "america", "united states", "los angeles", "silicon valley"],
  加拿大: ["ca", "canada", "toronto"],
  墨西哥: ["mx", "mexico"],
  巴西: ["br", "brazil"],
  英国: ["uk", "gb", "britain", "england", "london", "united kingdom"],
  爱尔兰: ["ie", "ireland", "dublin"],
  法国: ["fr", "france", "paris"],
  德国: ["de", "germany", "frankfurt"],
  荷兰: ["nl", "netherlands", "holland", "amsterdam"],
  比利时: ["be", "belgium"],
  瑞士: ["ch", "switzerland", "zurich"],
  奥地利: ["at", "austria", "vienna"],
  瑞典: ["se", "sweden", "stockholm"],
  挪威: ["no", "norway"],
  丹麦: ["dk", "denmark"],
  芬兰: ["fi", "finland", "helsinki"],
  意大利: ["it", "italy", "milan"],
  西班牙: ["es", "spain", "madrid"],
  葡萄牙: ["pt", "portugal"],
  波兰: ["pl", "poland", "warsaw"],
  捷克: ["cz", "czech"],
  俄罗斯: ["ru", "russia", "moscow"],
  南非: ["za", "south africa"],
  电信: ["ct", "telecom", "china telecom"],
  联通: ["cu", "unicom", "china unicom"],
  移动: ["cm", "cmcc", "mobile", "china mobile"],
  其他: ["other", "others", "custom"],
};

// 去掉空白与常见分隔符：查询和文本都折叠成同一种形状再做包含判断。
export function fold(text) {
  return String(text ?? "")
    .toLowerCase()
    .replace(/[\s\u3000/\\·、,，.．:：;；\-_+()（）[\]【】"']+/g, "");
}

// 逐字取音，遇到多音字地名先按词表定音（重庆 chongqing / 厦门 xiamen / 蚌埠 bengbu…）。
// 拼音表里没有的汉字跳过，纯文本匹配仍然能命中。
const phraseMax = Math.max(...Object.keys(pinyinPhrases).map((t) => [...t].length));

function syllables(text) {
  const source = String(text ?? "");
  const full = [];
  const initials = [];
  for (let i = 0; i < source.length; ) {
    let matched = 0;
    for (let len = Math.min(phraseMax, source.length - i); len >= 2; len--) {
      const phrase = pinyinPhrases[source.slice(i, i + len)];
      if (!phrase) continue;
      full.push(...phrase);
      initials.push(...phrase.map((syllable) => syllable[0]));
      i += len;
      matched = len;
      break;
    }
    if (matched) continue;
    const ch = source[i];
    const syllable = pinyinMap[ch];
    if (syllable) {
      full.push(syllable);
      initials.push(syllable[0]);
    } else if (/[a-z0-9]/i.test(ch)) {
      full.push(ch.toLowerCase());
      initials.push(ch.toLowerCase());
    }
    i += 1;
  }
  return { full: full.join(""), initials: initials.join("") };
}

// 一段文本的全部命中键：原文（折叠）、全拼、首字母，外加文本里出现的地区/运营商代号。
export function searchKeys(...parts) {
  const text = parts.filter(Boolean).join(" ");
  const keys = new Set([fold(text)]);
  const { full, initials } = syllables(text);
  if (full) keys.add(full);
  if (initials) keys.add(initials);
  for (const [term, codes] of Object.entries(aliases)) {
    if (!text.includes(term)) continue;
    for (const code of codes) keys.add(fold(code));
  }
  return [...keys];
}

// 关键词按空白拆开，每个词都要命中（AND）；任意一个命中键包含该词即算命中。
export function searchMatch(query, ...parts) {
  const tokens = String(query ?? "")
    .split(/[\s/、,，]+/)
    .map(fold)
    .filter(Boolean);
  if (!tokens.length) return true;
  const keys = searchKeys(...parts);
  return tokens.every((token) => keys.some((key) => key.includes(token)));
}
