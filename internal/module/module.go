// Package module is how a feature plugs into the Server: its database
// migrations, its HTTP routes and (later) its overview cards, public sections
// and task kinds. A module implements Module plus whichever of the optional
// interfaces below it needs; cmd/server lists the modules it ships.
//
// Task kinds register themselves with model.RegisterTaskKind; the Agent side
// has its own capability registry (internal/probe).
package module

import (
	"context"
	"net/http"

	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

// Module is one feature of the Server.
type Module interface {
	// ID is the module's stable name, also its key in schema_migrations.
	ID() string
}

// Migrator is a module that keeps tables in the main database.
type Migrator interface {
	Migrations() []storage.Migration
}

// Router is a module that serves HTTP endpoints.
type Router interface {
	Routes(Routes)
}

// Routes mounts handlers behind the API's own authentication. Patterns are
// net/http ServeMux patterns ("GET /api/v1/admin/x/{id}").
type Routes interface {
	// Public is reachable without signing in.
	Public(pattern string, h http.HandlerFunc)
	// Admin needs a signed-in administrator; writes also need a same-origin
	// request.
	Admin(pattern string, h http.HandlerFunc)
	// Agent needs a node token; node is the authenticated node's id.
	Agent(pattern string, h func(w http.ResponseWriter, r *http.Request, node string))
	// Audit records a privileged action for r in the audit log, with the
	// signed-in actor and the client address filled in. On an admin write it
	// replaces the generic entry the API would otherwise record.
	Audit(r *http.Request, e model.AuditEntry)
}

// Migrations is core's migrations followed by every module's, in the order
// the modules are listed.
func Migrations(modules []Module) []storage.Migration {
	list := storage.CoreMigrations()
	for _, m := range modules {
		if mig, ok := m.(Migrator); ok {
			list = append(list, mig.Migrations()...)
		}
	}
	return list
}

// Mount calls Routes on every module that serves endpoints.
func Mount(modules []Module, r Routes) {
	for _, m := range modules {
		if router, ok := m.(Router); ok {
			router.Routes(r)
		}
	}
}

// Card is one tile on the admin overview: a headline value, a tone and up to a
// few items that need attention. Page is the admin tab the tile opens.
type Card struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Page  string     `json:"page,omitempty"`
	Value string     `json:"value"`
	Note  string     `json:"note,omitempty"`
	Tone  string     `json:"tone,omitempty"` // "", "good", "warn", "bad"
	Items []CardItem `json:"items,omitempty"`
}

// CardItem is one line under a card: a node, a task or an entry to look at.
type CardItem struct {
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
	Tone  string `json:"tone,omitempty"`
}

// MaxCardItems bounds the lines one card lists; the value carries the total.
const MaxCardItems = 6

// Overviewer is a module that contributes cards to the admin overview. A
// module whose data cannot be read returns an error; the overview then shows
// that card as unavailable instead of failing the page.
type Overviewer interface {
	Overview(ctx context.Context) ([]Card, error)
}

// Overview collects the cards of every module that has them, in module order.
// A failing module yields one card that says so.
func Overview(ctx context.Context, modules []Module) []Card {
	out := []Card{}
	for _, m := range modules {
		o, ok := m.(Overviewer)
		if !ok {
			continue
		}
		cards, err := o.Overview(ctx)
		if err != nil {
			out = append(out, Card{ID: m.ID(), Title: m.ID(), Value: "读取失败", Note: err.Error(), Tone: "warn"})
			continue
		}
		out = append(out, cards...)
	}
	return out
}
