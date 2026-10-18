package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

type sessionPluginCatalog struct {
	Engine  string             `json:"engine"`
	Plugins []pluginspec.Entry `json:"plugins"`
	Allowed []string           `json:"allowed_marketplaces"`
	Any     bool               `json:"any_marketplace"`
}
type sessionPluginCore interface {
	Policy(context.Context, string) (sessionPluginCatalog, error)
	Catalog(context.Context, string, coreProof, string) (sessionPluginCatalog, error)
	Add(context.Context, string, coreProof, string, pluginspec.Entry) error
}
type httpSessionPluginCore struct {
	url, key string
	client   *http.Client
}

var errSessionPluginCore = errors.New("plugin settings could not be verified; sign in again or retry")

func (a *API) pluginCore() (sessionPluginCore, error) {
	if a.sessionPlugins != nil {
		return a.sessionPlugins, nil
	}
	if a.coreURL == "" || a.coreInternalKey == "" {
		return nil, errSessionPluginCore
	}
	return &httpSessionPluginCore{url: a.coreURL, key: a.coreInternalKey, client: coreHTTPClient(nil)}, nil
}
func (c *httpSessionPluginCore) call(ctx context.Context, path string, body any) (sessionPluginCatalog, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+path, bytes.NewReader(raw))
	if err != nil {
		return sessionPluginCatalog{}, errSessionPluginCore
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.key)
	resp, err := c.client.Do(req)
	if err != nil {
		return sessionPluginCatalog{}, errSessionPluginCore
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, maxPluginsResponse+1))
	var out sessionPluginCatalog
	if err != nil || len(raw) > maxPluginsResponse || resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || len(out.Plugins) > pluginspec.MaxEntries || len(out.Allowed) > 100 {
		return sessionPluginCatalog{}, errSessionPluginCore
	}
	for _, entry := range out.Plugins {
		if pluginspec.ValidateClaude(entry) != nil {
			return sessionPluginCatalog{}, errSessionPluginCore
		}
	}
	for _, market := range out.Allowed {
		if !pluginspec.ValidMarketplaceSource(market) {
			return sessionPluginCatalog{}, errSessionPluginCore
		}
	}
	return out, nil
}
func (c *httpSessionPluginCore) Catalog(ctx context.Context, account string, p coreProof, engine string) (sessionPluginCatalog, error) {
	out, err := c.call(ctx, "/internal/plugins/catalog", internalPluginsRequest{AccountID: account, SessionID: p.SessionID, TokenID: p.TokenID, Engine: engine})
	if err == nil && out.Engine != engine {
		err = errSessionPluginCore
	}
	return out, err
}
func (c *httpSessionPluginCore) Add(ctx context.Context, account string, p coreProof, engine string, e pluginspec.Entry) error {
	if p.SessionID == "" || p.TokenID != "" {
		return errSessionPluginCore
	}
	out, err := c.call(ctx, "/internal/plugins/add", map[string]any{"account_id": account, "session_id": p.SessionID, "engine": engine, "entry": e})
	if err == nil && out.Engine != engine {
		err = errSessionPluginCore
	}
	return err
}

func (c *httpSessionPluginCore) Policy(ctx context.Context, engine string) (sessionPluginCatalog, error) {
	out, err := c.call(ctx, "/internal/plugins/policy", map[string]string{"engine": engine})
	if err == nil && out.Engine != engine {
		err = errSessionPluginCore
	}
	return out, err
}
