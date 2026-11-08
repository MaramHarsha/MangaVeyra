package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Review fix: a Runware image without an imageUUID that a later poll repeats
// is one image, counted and charged once.
func TestReviewRunwareCountsRepeatedImagesWithoutIDOnce(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		first, second := phase4PNG(t, 512, 512), phase4PNG(t, 520, 520)
		var taskUUID string
		image := func(png []byte) map[string]any {
			return map[string]any{"taskType": "imageInference", "taskUUID": taskUUID, "status": "success", "imageBase64Data": b64(png), "cost": 0.0011}
		}
		doer := (&routeDoer{}).on(http.MethodPost, runwareURL, func(_ *http.Request, body []byte) (*http.Response, error) {
			var tasks []map[string]any
			_ = json.Unmarshal(body, &tasks)
			switch tasks[0]["taskType"] {
			case "imageInference":
				taskUUID, _ = tasks[0]["taskUUID"].(string)
				return httpJSON(200, map[string]any{"data": []any{image(first)}}, nil), nil
			case "getResponse":
				// Every result so far, again without image IDs.
				return httpJSON(200, map[string]any{"data": []any{image(first), image(second)}}, nil), nil
			}
			return httpJSON(400, map[string]any{}, nil), nil
		})
		m, f := imageTestModule(t, d, doer)
		m.image.pollEvery = time.Millisecond
		f.createKindProvider(t, m.providers, providerKindRunware, providerScopeServer, "Runware")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 2}})
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		result, err := m.image.GenerateImage(context.Background(), req)
		if err != nil || len(result.Images) != 2 || math.Abs(result.Actual.USD-0.0022) > 1e-12 {
			t.Fatalf("result = %d images, actual %v, %v", len(result.Images), result.Actual.USD, err)
		}
	})
}
