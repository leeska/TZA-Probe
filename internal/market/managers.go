package market

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type managerRecord struct {
	Archive   []byte           `json:"archive"`
	Signature tzapkg.Signature `json:"signature"`
}

func (c *Client) managerPath() string {
	return filepath.Join(c.dir, "plugin-managers", tzapkg.OfficialExecutorID+".json")
}

func (c *Client) verifyManager(record managerRecord) (Result, error) {
	if err := record.Signature.Verify(c.keys, record.Archive); err != nil {
		return Result{}, rejected("插件管理部分签名无效")
	}
	manifest, err := tzapkg.OpenExecutor(record.Archive)
	if err != nil || record.Signature.Package != tzapkg.OfficialExecutorID || manifest.Version != record.Signature.Version {
		return Result{}, rejected("插件管理部分身份或版本不符")
	}
	info, err := tzapkg.Inspect(record.Archive)
	if err != nil {
		return Result{}, err
	}
	return Result{ID: manifest.ID, Type: tzapkg.TypeExecutor, Kind: "agent", Name: manifest.Name, Version: manifest.Version, Source: tzapkg.SourceMarket, Permissions: info.Permissions}, nil
}

func (c *Client) installManager(archive []byte, signature tzapkg.Signature) (Result, error) {
	if c.dir == "" {
		return Result{}, errors.New("未配置主控插件数据目录")
	}
	record := managerRecord{Archive: archive, Signature: signature}
	result, err := c.verifyManager(record)
	if err != nil {
		return Result{}, err
	}
	if _, err := c.plugins.Plugin(result.ID); err == nil {
		return Result{}, model.Conflict("同名本地插件已存在，请先移除后再安装管理部分")
	}
	for _, theme := range c.themes.Installed() {
		if theme.ID == result.ID {
			return Result{}, model.Conflict("同名本地主题已存在，请先移除后再安装管理部分")
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return Result{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	directory := filepath.Dir(c.managerPath())
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Result{}, err
	}
	file, err := os.CreateTemp(directory, ".manager-")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return Result{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return Result{}, err
	}
	if err := file.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(file.Name(), c.managerPath()); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (c *Client) Managers() []Result {
	results := []Result{}
	if c == nil || c.dir == "" {
		return results
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Lstat(c.managerPath())
	if err != nil || !info.Mode().IsRegular() || info.Size() > tzapkg.MaxUpload*2 {
		return results
	}
	raw, err := os.ReadFile(c.managerPath())
	if err != nil {
		return results
	}
	var record managerRecord
	if json.Unmarshal(raw, &record) != nil {
		return results
	}
	if result, err := c.verifyManager(record); err == nil {
		results = append(results, result)
	}
	return results
}

func (c *Client) RemoveManager(id string) error {
	if c == nil || c.dir == "" || id != tzapkg.OfficialExecutorID {
		return model.NotFound("没有安装这个插件管理部分")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(c.managerPath()); errors.Is(err, os.ErrNotExist) {
		return model.NotFound("没有安装这个插件管理部分")
	} else {
		return err
	}
}
