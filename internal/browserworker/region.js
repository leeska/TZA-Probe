        const normalize = value => value.normalize("NFKC").replace(/\s+/gu, " ").trim().toLocaleLowerCase();
        const labels = new Set();
        if (source === "rendered_terms_country") {
            for (const marker of document.querySelectorAll('[data-name="country-version"]')) {
                const parent = marker.parentElement;
                if (!marker.checkVisibility({checkOpacity:true, checkVisibilityCSS:true}) || !parent.checkVisibility({checkOpacity:true, checkVisibilityCSS:true})) continue;
                const text = parent.innerText.replace(marker.innerText, "").trim();
                if (text && text.length <= 100) labels.add(text);
            }
        } else if (source === "rendered_country_selector") {
            for (const element of document.querySelectorAll('[role="combobox"], select')) {
                if (!element.checkVisibility({checkOpacity:true, checkVisibilityCSS:true})) continue;
                const text = element.tagName === "SELECT" ? element.selectedOptions[0]?.textContent : element.innerText;
                if (text) labels.add(text.replace(/[\u200b-\u200f\ufeff]/gu, "").trim());
            }
        } else for (const footer of document.querySelectorAll('footer, [role="contentinfo"], #footer, #fbar')) {
            for (const element of footer.querySelectorAll('div, span, p')) {
                if (element.closest('a, button, select, [role="button"]') || element.querySelector('a, button, input, select, [role="button"]') || !element.checkVisibility({checkOpacity:true, checkVisibilityCSS:true})) continue;
                const text = element.innerText.trim();
                if (text && text.length <= 100) labels.add(text);
            }
        }
        const locales = [...new Set([document.documentElement.lang, ...navigator.languages, "en"].filter(Boolean))];
        const names = new Map();
        for (const locale of locales) {
            for (const style of ["long", "short"]) {
                let display;
                try { display = new Intl.DisplayNames([locale], {type:"region", style, fallback:"none"}); } catch { continue; }
                for (let first = 65; first <= 90; first++) {
                    for (let second = 65; second <= 90; second++) {
                        const code = String.fromCharCode(first, second);
                        if (["ZZ", "EU", "EZ", "QO", "UN"].includes(code) || new Intl.Locale("und-" + code).region !== code) continue;
                        const name = display.of(code);
                        if (!name || name === code) continue;
                        const key = normalize(name);
                        if (!names.has(key)) names.set(key, new Set());
                        names.get(key).add(code);
                    }
                }
            }
        }
        const candidates = new Set();
        const matched = [];
        for (const label of labels) {
            const codes = names.get(normalize(label));
            if (!codes) continue;
            matched.push(label);
            for (const code of codes) candidates.add(code);
        }
        return {source, candidates:[...candidates].sort(), labels:matched.slice(0, 10)};
