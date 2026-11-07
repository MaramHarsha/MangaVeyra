package gen

import (
	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// SetTextServiceForTest replaces only the private dispatch dependency of the
// concrete service already registered by the real app module.
func SetTextServiceForTest(target Service, text ai.TextService) {
	target.(*service).text = text
}

// SetStudioServiceForTest lets transaction tests wrap the app-owned Studio
// service without registering a duplicate core service.
func SetStudioServiceForTest(target Service, studioService studio.Service) {
	target.(*service).studio = studioService
}
