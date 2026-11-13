// Package module is how a feature plugs into the Server: its database
// migrations, its HTTP routes and (later) its overview cards, public sections
// and task kinds. A module implements Module plus whichever of the optional
// interfaces below it needs; cmd/server lists the modules it ships.
//
// Task kinds register themselves with model.RegisterTaskKind; the Agent side
// has its own capability registry (internal/probe).
package module

import (
	"net/http"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/storage"
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
