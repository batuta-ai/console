package accounts

import "github.com/batuta-ai/console/internal/adapters"

// Catalog is the adapter capability table. Isolation is never inferred
// from another product; each CLI is qualified on its own.
var Catalog = adapters.All

var New = adapters.NewAccount
var Get = adapters.Get
