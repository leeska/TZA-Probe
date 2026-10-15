package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/leeska/TZA-Probe/internal/catalog"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// Plugins is the installed-plugin port (internal/plugin.Store). Plugin
// answers an error for a missing id or a plugin.json that no longer parses.
type Plugins interface {
	List() []tzapkg.Plugin
	Plugin(id string) (tzapkg.Plugin, error)
	Remove(id string) error
}

// PluginView is an installed plugin as the panel and MCP list it: what it is,
// where it came from and what it may do. A notify plugin carries its settings
// form; a target set only its size (the targets show up in the catalog).
type PluginView struct {
	ID          string           `json:"id"`
	Kind        string           `json:"kind"`
	Name        string           `json:"name"`
	Hint        string           `json:"hint,omitempty"`
	Version     string           `json:"version,omitempty"`
	Author      string           `json:"author,omitempty"`
	Source      string           `json:"source,omitempty"`
	Permissions []string         `json:"permissions"`
	Settings    []tzapkg.Setting `json:"settings,omitempty"`
	Targets     int              `json:"targets,omitempty"`
}

// ViewPlugin describes one plugin.
func ViewPlugin(p tzapkg.Plugin) PluginView {
	v := PluginView{ID: p.ID, Kind: p.Kind, Name: p.Name, Hint: p.Hint, Version: p.Version, Author: p.Author, Source: p.Source, Permissions: p.Permissions(), Targets: len(p.Targets)}
	if v.Permissions == nil {
		v.Permissions = []string{}
	}
	if p.Notify != nil {
		v.Settings = p.Notify.Settings
	}
	return v
}

// PluginList is every installed plugin.
func (s *Service) PluginList() []PluginView {
	out := []PluginView{}
	if s.Plugins == nil {
		return out
	}
	for _, p := range s.Plugins.List() {
		out = append(out, ViewPlugin(p))
	}
	return out
}

// RemovePlugin deletes an installed plugin, unless it is the notification
// channel in use: switching the channel first is the admin's call, the
// Server never silently turns notifications off.
func (s *Service) RemovePlugin(ctx context.Context, id string) error {
	if s.Plugins == nil {
		return model.NotFound("没有安装这个插件")
	}
	if c, err := s.loadNotification(ctx); err == nil && c.Channel == "plugin" && c.Plugin == id {
		return model.Conflict(fmt.Sprintf("插件 %s 正被用作通知渠道，先在「告警与通知 → 配置出口」换成别的渠道再删除", id))
	}
	if err := s.Plugins.Remove(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.NotFound("没有安装这个插件：" + id)
		}
		return err
	}
	return nil
}

// CatalogTarget is one entry of the task editor's target catalog. Targets
// from a target-set plugin carry its id and name, and an id namespaced as
// "plugin:<plugin id>:<target id>" so it never collides with the built-in list.
type CatalogTarget struct {
	ID         string `json:"id"`
	Family     string `json:"family"`
	Region     string `json:"region"`
	Carrier    string `json:"carrier"`
	Host       string `json:"host"`
	IP         string `json:"ip,omitempty"`
	Port       int    `json:"port"`
	Plugin     string `json:"plugin,omitempty"`
	PluginName string `json:"plugin_name,omitempty"`
}

// CatalogSource is one target-set plugin merged into the catalog.
type CatalogSource struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Catalog is the built-in snapshot plus the installed target sets.
type Catalog struct {
	Source       string          `json:"source"`
	SnapshotDate string          `json:"snapshot_date"`
	Verification string          `json:"verification,omitempty"`
	Targets      []CatalogTarget `json:"targets"`
	Plugins      []CatalogSource `json:"plugins"`
}

var builtinCatalog = sync.OnceValues(func() (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(catalog.JSON, &c); err != nil {
		return Catalog{}, err
	}
	return c, nil
})

// Catalog merges the installed target-set plugins into the built-in target
// catalog. Removing a plugin only takes its targets out of here; tasks that
// already use them keep running.
func (s *Service) Catalog() (Catalog, error) {
	base, err := builtinCatalog()
	if err != nil {
		return Catalog{}, err
	}
	out := base
	out.Targets = append([]CatalogTarget{}, base.Targets...)
	out.Plugins = []CatalogSource{}
	if s.Plugins == nil {
		return out, nil
	}
	for _, p := range s.Plugins.List() {
		if p.Kind != tzapkg.KindTargets {
			continue
		}
		out.Plugins = append(out.Plugins, CatalogSource{ID: p.ID, Name: p.Name, Count: len(p.Targets)})
		for _, t := range p.Targets {
			out.Targets = append(out.Targets, CatalogTarget{ID: "plugin:" + p.ID + ":" + t.ID, Family: t.Family, Region: t.Region, Carrier: t.Carrier, Host: t.Host, Port: t.Port, Plugin: p.ID, PluginName: p.Name})
		}
	}
	return out, nil
}

// pluginTargets are the targets of every installed target set.
func (s *Service) pluginTargets() []tzapkg.Target {
	if s.Plugins == nil {
		return nil
	}
	var out []tzapkg.Target
	for _, p := range s.Plugins.List() {
		if p.Kind == tzapkg.KindTargets {
			out = append(out, p.Targets...)
		}
	}
	return out
}
