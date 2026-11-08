package reader

import "github.com/MaramHarsha/MangaVeyra/internal/publish"

// IslandKeys exposes the island's catalogue keys to the locale test.
var IslandKeys = islandKeys

// SetPublish replaces the publish.Service the reader reads through (tests
// use it to check the reader against other Publications behaviours).
func SetPublish(m *Module, svc publish.Service) { m.pub = svc }

// Publish returns the publish.Service the reader reads through.
func Publish(m *Module) publish.Service { return m.pub }
