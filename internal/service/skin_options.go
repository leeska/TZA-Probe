package service

import (
	"context"
	"encoding/json"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// SkinOptions are the administrator's theme settings, by skin id and then by
// setting id (see tzapkg.SkinSetting). They apply to every visitor and are
// sent to the page with the skin list. A value the theme no longer declares is
// simply ignored by the page, so nothing here has to follow package updates.
func (s *Service) SkinOptions(ctx context.Context) (map[string]map[string]any, error) {
	raw, err := s.Store.Setting(ctx, model.SettingSkinOptions)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	if raw != "" {
		// A hand-edited, unreadable value is treated as "nothing set".
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out, nil
}

// SetSkinOptions merges patch into one skin's settings after checking it
// against the settings the skin declares, and returns that skin's values.
func (s *Service) SetSkinOptions(ctx context.Context, skin string, settings []tzapkg.SkinSetting, patch map[string]json.RawMessage) (map[string]any, error) {
	if len(settings) == 0 {
		return nil, model.Invalid("这个主题没有可调的设置")
	}
	if len(patch) == 0 {
		return nil, model.Invalid("至少给一项设置")
	}
	values, err := tzapkg.SkinOptionValues(settings, patch)
	if err != nil {
		return nil, model.Invalid(err.Error())
	}
	all, err := s.SkinOptions(ctx)
	if err != nil {
		return nil, err
	}
	mine := all[skin]
	if mine == nil {
		mine = map[string]any{}
	}
	for k, v := range values {
		mine[k] = v
	}
	all[skin] = mine
	raw, err := json.Marshal(all)
	if err != nil {
		return nil, err
	}
	if err := s.Store.SetSetting(ctx, model.SettingSkinOptions, string(raw)); err != nil {
		return nil, err
	}
	return mine, nil
}
