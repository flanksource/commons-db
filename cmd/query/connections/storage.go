package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flanksource/commons-db/models"
)

type StorageLayer struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Path     string `json:"path,omitempty"`
	Writable bool   `json:"writable"`
}

type KEKStatus struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type StorageInfo struct {
	Layers       []StorageLayer `json:"layers"`
	DefaultLayer string         `json:"defaultLayer"`
	KEK          KEKStatus      `json:"kek"`
}

type LayeredConnectionProvider interface {
	WritableConnectionProvider
	StorageLayers() []StorageLayer
	DefaultLayer() string
}

func connectionLayer(c *models.Connection) string {
	if c.Layer != "" {
		return c.Layer
	}
	if c.Virtual {
		return "virtual"
	}
	return "db"
}

func (s *Service) Storage(ctx context.Context) StorageInfo {
	info := StorageInfo{Layers: []StorageLayer{}, KEK: KEKStatus{State: "not_configured", Detail: "No KEK is configured for this connection store."}}
	if s.database != nil {
		info.DefaultLayer = "db"
		info.Layers = append(info.Layers, StorageLayer{Name: "db", Label: "Database", Writable: true})
	}
	if provider, ok := s.virtual.(LayeredConnectionProvider); ok {
		info.Layers = append(info.Layers, provider.StorageLayers()...)
		if info.DefaultLayer == "" {
			info.DefaultLayer = provider.DefaultLayer()
		}
	}
	if s.kekStatus != nil {
		info.KEK = s.kekStatus(ctx)
	}
	return info
}

func (s *Service) storageHandler(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != prefix+"/connections/storage" || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(s.Storage(r.Context())); err != nil {
			panic(err)
		}
	})
}

func (s *Service) saveLayer(ctx context.Context, c *models.Connection) (*models.Connection, error) {
	provider, ok := s.virtual.(LayeredConnectionProvider)
	if !ok {
		return nil, fmt.Errorf("connection storage layer %q is unavailable", c.Layer)
	}
	return provider.SaveConnection(ctx, c)
}
