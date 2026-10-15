package mcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/leeska/TZA-Probe/internal/market"
)

// marketTools mirror the panel's 市场 page: browse TZA-CC and install or
// update a package. They reach an outside service, so openWorldHint is set.
var marketTools = []tool{
	{
		Name:        "search_market",
		Description: "浏览 TZA-CC 市场（Server 须用 --cc-url 配置了市场）：按 type（skin 主题 / characters 角色包 / plugin 插件）与关键词 q 搜索；插件条目带 kind（notify 通知渠道 / targets 探测目标集）与 permissions（它能做的事，安装前请让用户确认），每项标出本机已装的版本与来源（installed.source：market 市场 / local 本地上传）。传 id 返回该包详情：说明、截图地址、版本列表（更新说明、需要的 Probe 版本、compatible 是否兼容本机）。传 updates=true 只列市场装的包里有兼容新版的。",
		InputSchema: schema(map[string]any{
			"type":    map[string]any{"type": "string", "enum": []string{"skin", "characters", "plugin"}, "description": "只看主题（skin）、角色包（characters）或插件（plugin）；不传都列"},
			"q":       map[string]any{"type": "string", "description": "关键词，最多 64 字"},
			"page":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "description": "页码，默认 1；结果的 more=true 表示还有下一页"},
			"id":      map[string]any{"type": "string", "description": "包 id：返回这个包的详情与版本列表"},
			"updates": map[string]any{"type": "boolean", "description": "只列有更新的已装市场包（id、已装版本、最新版本）"},
		}),
		Annotations: openWorld(annotations("Search theme market", true, false, true)),
	},
	{
		Name:        "install_market_package",
		Description: "从 TZA-CC 市场安装或更新主题包 / 角色包 / 插件（面板「市场」页的安装 / 更新按钮）：下载后核对大小、SHA-256、平台签名，以及包内清单（theme.json / plugin.json）的 id 与版本，全部通过才安装，否则整包拒收、不留文件。同 id 的已装包（市场或本地上传）会被替换。安装插件前先用 search_market 取 permissions 请用户确认；装好的通知插件要再用 update_settings（channel=plugin）选用才会发通知。version 省略即装当前 Probe 能用的最新版。",
		InputSchema: schema(map[string]any{
			"id":      map[string]any{"type": "string", "description": "包 id，来自 search_market"},
			"version": map[string]any{"type": "string", "pattern": `^\d+\.\d+\.\d+$`, "description": "版本号 MAJOR.MINOR.PATCH；省略即最新兼容版"},
		}, "id"),
		Annotations: openWorld(annotations("Install market package", false, true, true)),
	},
}

func openWorld(notes map[string]any) map[string]any {
	notes["openWorldHint"] = true
	return notes
}

func (s *Server) callMarketTool(ctx context.Context, params call) (map[string]any, *rpcError, int) {
	switch params.Name {
	case "search_market":
		var args struct {
			Type    string `json:"type"`
			Q       string `json:"q"`
			Page    int    `json:"page"`
			ID      string `json:"id"`
			Updates bool   `json:"updates"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		switch {
		case args.Updates:
			updates, err := s.Market.Updates(ctx)
			if err != nil {
				return marketFailure("检查更新失败", err)
			}
			return toolResult(map[string]any{"updates": updates, "count": len(updates)}), nil, 0
		case args.ID != "":
			pkg, err := s.Market.Package(ctx, args.ID)
			if err != nil {
				return marketFailure("读取市场包失败", err)
			}
			return toolResult(map[string]any{"package": pkg}), nil, 0
		}
		out, err := s.Market.Catalog(ctx, market.Query{Type: args.Type, Q: args.Q, Page: args.Page})
		if err != nil {
			return marketFailure("读取市场目录失败", err)
		}
		return toolResult(map[string]any{"items": out.Items, "count": len(out.Items), "page": out.Page, "more": out.More}), nil, 0

	case "install_market_package":
		var args struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if e := arguments(params.Arguments, &args); e != nil {
			return nil, e, http.StatusOK
		}
		if args.ID == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "缺少 id；先用 search_market 查包 id"}, http.StatusOK
		}
		m, err := s.Market.Install(ctx, args.ID, args.Version)
		if err != nil {
			return marketFailure("安装失败", err)
		}
		out := map[string]any{"id": m.ID, "name": m.Name, "type": m.Type, "version": m.Version, "source": m.Source, "installed": true}
		if m.Kind != "" {
			out["kind"], out["permissions"] = m.Kind, m.Permissions
		}
		return toolResult(out), nil, 0
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + params.Name}, http.StatusOK
}

// marketFailure: input the caller got wrong is -32602 (through failure),
// everything else (no market configured, CC unreachable, a package that fails
// verification) is a readable tool error.
func marketFailure(message string, err error) (map[string]any, *rpcError, int) {
	if errors.Is(err, market.ErrNotConfigured) || errors.Is(err, market.ErrNoTrustedKeys) {
		return toolError(err.Error()), nil, 0
	}
	return failure(message, err)
}
