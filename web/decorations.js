import { disabledSources } from "./skins.js";

// 按节点挑装饰（docs/THEMES.md「按节点挑装饰」）：主题包与依附它的附加包在清单里声明 decorations，
// 每项写匹配规则（nodes / regions / default）、几张图片和若干数值。面板只负责挑：
//   节点自己指定的「包 id/项 id」→ 节点名在 nodes 里 → 所在地区在 regions 里 → default，
//   每一步附加包都排在主题自带的项前面（装附加包就是为了替换），最后由主题自带的 default 兜底。
// 挑中的项写成卡壳上的 data-deco="<包>/<项>"、data-deco-pack="<包>"，图片与数值写成 --deco-<名字>；
// 面板不认识这些名字的含义，怎么画由主题的样式表决定。
//
// 清单来自 /theme-default.js 的 window.__tzaThemes（后台装 / 删之后 skins.js 的 registerThemes 会换掉它），
// 所以每次现读，不缓存。

const lower = (v) => String(v || "").trim().toLowerCase();
const themes = () => window.__tzaThemes || [];

// 一套主题的装饰来源：依附它的附加包（按 id 排，清单本来就是这个顺序）在前，主题自己在后。
function sources(skin) {
  const host = themes().find((t) => t.id === skin && t.type !== "addon");
  if (!host) return { host: null, all: [] };
  const addons = themes().filter((t) => t.type === "addon" && t.for === skin);
  return { host, all: [...addons, host] };
}

const items = (pkg) => (Array.isArray(pkg?.decorations) ? pkg.decorations.map((d) => ({ pkg, d, key: `${pkg.id}/${d.id}` })) : []);

// 给一台节点挑装饰，挑不到返回 null（这套主题没有装饰项）。
export function pickDecoration(node, skin = document.documentElement.dataset.skin) {
  const { host, all } = sources(skin);
  if (!host) return null;
  const off = disabledSources(skin);
  const pool = all.filter((pkg) => !off.has(pkg.id)).flatMap(items);
  const pick = node?.decoration;
  const chosen = pick && pool.find((x) => x.key === pick);
  if (chosen) return chosen;
  const name = lower(node?.name);
  const region = String(node?.region || "").trim();
  return (
    pool.find((x) => (x.d.nodes || []).some((n) => lower(n) === name)) ||
    (region && pool.find((x) => (x.d.regions || []).includes(region))) ||
    pool.find((x) => x.d.default) ||
    // 主题自带的 default 是最后的兜底：设置里关掉了主题自带的项，它也照样接住。
    items(host).find((x) => x.d.default) ||
    null
  );
}

// 挑中项要写到卡壳上的变量：图片是包里的地址（服务端校验过路径，只含安全字符），数值原样。
export function decorationVars(x) {
  const out = {};
  if (!x) return out;
  for (const [name, file] of Object.entries(x.d.images || {})) out[`--deco-${name}`] = `url("/themes/${x.pkg.id}/${file}?v=${x.pkg.rev || ""}")`;
  for (const [name, value] of Object.entries(x.d.vars || {})) out[`--deco-${name}`] = String(value);
  return out;
}

// 后台节点表单里「主题装饰」下拉的候选：每套已装主题与附加包里的全部装饰项。
export function decorationChoices() {
  const byId = new Map(themes().map((t) => [t.id, t]));
  return themes()
    .filter((t) => Array.isArray(t.decorations) && t.decorations.length)
    .flatMap((t) => {
      const host = t.type === "addon" ? byId.get(t.for) : t;
      const source = t.type === "addon" ? `${host?.name || t.for} · ${t.name}` : t.name;
      return t.decorations.map((d) => ({ key: `${t.id}/${d.id}`, name: d.name, source }));
    });
}
