package assets_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSaveReadURLDelete(t *testing.T) {
	ta := testutil.NewTestApp(t)
	svc := assets.From(ta.Deps)
	ctx := context.Background()
	data := pngBytes(t, 32, 48)

	a, err := svc.Save(ctx, assets.SaveParams{OwnerID: ta.Admin.ID, Kind: "layer", Source: assets.SourceAI, Data: data, Meta: map[string]string{"seed": "7"}})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if a.ContentType != "image/png" || a.Width != 32 || a.Height != 48 || a.Bytes != int64(len(data)) || len(a.SHA256) != 64 {
		t.Fatalf("unexpected asset: %+v", a)
	}
	got, row, err := svc.Read(ctx, a.ID, 0)
	if err != nil || !bytes.Equal(got, data) || row.Kind != "layer" || row.Source != assets.SourceAI {
		t.Fatalf("read: %v %+v", err, row)
	}
	if !strings.Contains(string(row.Meta), `"seed":"7"`) {
		t.Fatalf("meta not stored: %s", row.Meta)
	}
	if u := svc.URL(a, 0); !strings.HasPrefix(u, "/media/") {
		t.Fatalf("url: %q", u)
	}
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, a.ID); err != assets.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSaveTxRollsBackRow(t *testing.T) {
	ta := testutil.NewTestApp(t)
	svc := assets.From(ta.Deps)
	ctx := context.Background()
	var id string
	err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		a, err := svc.SaveTx(ctx, tx, assets.SaveParams{Kind: "export", Source: assets.SourceSystem, Data: []byte("%PDF-1.7 test")})
		if err != nil {
			return err
		}
		id = a.ID
		return context.Canceled // force rollback
	})
	if err != context.Canceled {
		t.Fatalf("want rollback error, got %v", err)
	}
	if _, err := svc.Get(ctx, id); err != assets.ErrNotFound {
		t.Fatalf("row should not exist after rollback, got %v", err)
	}
}

func TestSaveRejectsEmptyAndBadSource(t *testing.T) {
	ta := testutil.NewTestApp(t)
	svc := assets.From(ta.Deps)
	if _, err := svc.Save(context.Background(), assets.SaveParams{}); err == nil {
		t.Fatal("empty data must fail")
	}
	if _, err := svc.Save(context.Background(), assets.SaveParams{Data: []byte("x"), Source: "elsewhere"}); err == nil {
		t.Fatal("unknown source must fail")
	}
}
