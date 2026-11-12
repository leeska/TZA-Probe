import { searchPicker, wireSearchPickers } from "./search-picker.js";

const languages = "en-US en-GB en-CA en-AU en-NZ en-SG en-IN zh-CN zh-TW zh-HK zh-SG zh-Hans zh-Hant ja-JP ko-KR de-DE de-AT de-CH fr-FR fr-CA fr-CH es-ES es-MX es-AR es-CL es-CO es-US pt-BR pt-PT ru-RU uk-UA be-BY it-IT nl-NL nl-BE sv-SE nb-NO nn-NO da-DK fi-FI is-IS pl-PL cs-CZ sk-SK hu-HU ro-RO bg-BG hr-HR sr-RS sl-SI el-GR tr-TR ar-SA ar-AE ar-EG he-IL fa-IR hi-IN bn-BD ta-IN te-IN ur-PK id-ID ms-MY th-TH vi-VN fil-PH km-KH lo-LA my-MM ne-NP si-LK sw-KE af-ZA zu-ZA et-EE lv-LV lt-LT ka-GE hy-AM az-AZ kk-KZ uz-UZ mn-MN mk-MK sq-AL bs-BA ca-ES eu-ES gl-ES cy-GB ga-IE mt-MT".split(" ");
const countries = "US CA GB DE FR NL JP KR SG HK TW CN MO AU NZ IN BR MX ZA AE AT BE CH CZ DK ES FI GR HU IE IS IT NO PL PT RO SE TR UA RU AR CL CO PE VE UY BO EC PY CR PA GT CU DO JM TT BS BB BZ SV HN NI HT AG DM GD KN LC VC AD AL BA BG BY CY EE HR LI LT LU LV MC MD ME MK MT RS SI SK SM VA XK AM AZ GE IL JO KW LB OM QA SA BH IQ IR YE PS EG DZ MA TN LY SD SS ET KE NG GH UG TZ RW SN CI CM CD CG AO ZM ZW MZ BW NA SZ LS MW MG MU SC CV ST GQ GA GN GW LR SL TG BJ BF ML NE TD MR SO DJ ER KM GM BI CF AF BD BT BN KH ID LA LK MM MV MY NP PH PK TH TL VN KZ KG TJ TM UZ MN KP FJ PG SB TO TV VU WS KI NR FM MH PW CK NU AS GU MP NC PF WF GY SR GF AW CW SX BQ BM KY TC VG VI FK GL FO GI GG JE IM AX AI MS SH AC IO RE YT GP MQ BL MF PM PR EH CF AQ BV CC CX GS HM NF PN SJ TK UM WF".split(" ");
const zoneCities = {
  UTC: "协调世界时 世界标准时间", "Asia/Shanghai": "中国 上海 北京", "Asia/Hong_Kong": "中国 香港", "Asia/Taipei": "台湾 台北", "Asia/Macau": "中国 澳门", "Asia/Macao": "中国 澳门", "Asia/Singapore": "新加坡", "Asia/Tokyo": "日本 东京", "Asia/Seoul": "韩国 首尔", "Asia/Kolkata": "印度 加尔各答", "Asia/Calcutta": "印度 加尔各答", "Asia/Bangkok": "泰国 曼谷", "Asia/Dubai": "阿联酋 迪拜", "Asia/Kuala_Lumpur": "马来西亚 吉隆坡", "Asia/Jakarta": "印度尼西亚 雅加达", "Asia/Manila": "菲律宾 马尼拉", "Asia/Ho_Chi_Minh": "越南 胡志明市", "Asia/Saigon": "越南 胡志明市", "Asia/Jerusalem": "以色列 耶路撒冷", "Asia/Kathmandu": "尼泊尔 加德满都", "Asia/Katmandu": "尼泊尔 加德满都",
  "America/New_York": "美国 纽约", "America/Chicago": "美国 芝加哥", "America/Denver": "美国 丹佛", "America/Los_Angeles": "美国 洛杉矶", "America/Phoenix": "美国 凤凰城", "America/Anchorage": "美国 安克雷奇", "America/Toronto": "加拿大 多伦多", "America/Vancouver": "加拿大 温哥华", "America/Mexico_City": "墨西哥 墨西哥城", "America/Sao_Paulo": "巴西 圣保罗", "America/Argentina/Buenos_Aires": "阿根廷 布宜诺斯艾利斯", "America/Buenos_Aires": "阿根廷 布宜诺斯艾利斯",
  "Europe/London": "英国 伦敦", "Europe/Berlin": "德国 柏林", "Europe/Paris": "法国 巴黎", "Europe/Amsterdam": "荷兰 阿姆斯特丹", "Europe/Zurich": "瑞士 苏黎世", "Europe/Stockholm": "瑞典 斯德哥尔摩", "Europe/Oslo": "挪威 奥斯陆", "Europe/Helsinki": "芬兰 赫尔辛基", "Europe/Warsaw": "波兰 华沙", "Europe/Dublin": "爱尔兰 都柏林", "Europe/Madrid": "西班牙 马德里", "Europe/Rome": "意大利 罗马", "Europe/Istanbul": "土耳其 伊斯坦布尔", "Europe/Moscow": "俄罗斯 莫斯科", "Europe/Kyiv": "乌克兰 基辅", "Europe/Kiev": "乌克兰 基辅", "Australia/Sydney": "澳大利亚 悉尼", "Australia/Melbourne": "澳大利亚 墨尔本", "Australia/Perth": "澳大利亚 珀斯", "Pacific/Auckland": "新西兰 奥克兰", "Pacific/Honolulu": "美国 夏威夷 檀香山", "Africa/Johannesburg": "南非 约翰内斯堡", "Africa/Cairo": "埃及 开罗",
};
const catalog = new Map();
export const enableCountries = "US CA GB DE FR NL SE NO FI CH AT IT ES PT IE PL CZ AU NZ JP KR SG HK TW IN BR MX ZA AE".split(" ");

function displayNames(language, type) {
  try { return new Intl.DisplayNames([language], { type }); } catch { return { of: value => value }; }
}

export function environmentChoices(kind) {
  if (catalog.has(kind)) return catalog.get(kind);
  let choices;
  if (kind === "timezone") {
    let zones = [];
    try { zones = Intl.supportedValuesOf("timeZone"); } catch {}
    const values = [...new Set(["UTC", ...Object.keys(zoneCities), ...zones])];
    choices = values.filter(value => {
      try { new Intl.DateTimeFormat("en", { timeZone: value }); return true; } catch { return false; }
    }).map(value => {
      let label = zoneCities[value];
      if (!label) {
        try { label = new Intl.DateTimeFormat("zh-CN", { timeZone: value, timeZoneName: "longGeneric" }).formatToParts(new Date()).find(part => part.type === "timeZoneName")?.value; } catch {}
      }
      return { value, label: label || value, search: value.replaceAll("_", " ") };
    });
  } else {
    const type = kind === "language" ? "language" : "region";
    const chinese = displayNames("zh-CN", type);
    const english = displayNames("en", type);
    choices = [...new Set(kind === "language" ? languages : countries)].map(value => ({ value, label: chinese.of(value) || value, search: english.of(value) || value }));
  }
  catalog.set(kind, choices);
  return choices;
}

export function environmentPicker(label, name, value, kind, { allowedValues, allowCustom = true } = {}) {
  const choices = environmentChoices(kind).filter(choice => !allowedValues || allowedValues.includes(choice.value));
  return searchPicker(label, name, value, choices, { allowCustom, placeholder: kind === "timezone" ? "选择时区，或输入城市 / IANA 名称搜索" : kind === "language" ? "选择语言，或输入名称 / 语言代码搜索" : "选择地区，或输入名称 / 两位代码搜索" });
}

export { wireSearchPickers };
