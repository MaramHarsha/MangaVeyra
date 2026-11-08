package gen

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

func loraFixture(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	header := map[string]any{
		"unet.attn.lora.down.weight": map[string]any{"dtype": "F32", "shape": []int{2, 2}, "data_offsets": []int{0, 16}},
		"unet.attn.lora.up.weight":   map[string]any{"dtype": "F32", "shape": []int{2, 2}, "data_offsets": []int{16, 32}},
	}
	if change != nil {
		change(header)
	}
	b, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8+len(b)+32)
	binary.LittleEndian.PutUint64(data, uint64(len(b)))
	copy(data[8:], b)
	return data
}
func TestTrainingArtifactRejectsMockMalformedAndUnpairedWeights(t *testing.T) {
	// This validates artifact structure, never a GPU-training quality claim.
	if !realLoRA(loraFixture(t, nil)) {
		t.Fatal("valid paired LoRA rejected")
	}
	for _, change := range []func(map[string]any){
		func(h map[string]any) { h["__metadata__"] = map[string]string{"mangaveyra.mock": "true"} },
		func(h map[string]any) { delete(h, "unet.attn.lora.up.weight") },
		func(h map[string]any) { h["unet.attn.lora.up.weight"].(map[string]any)["data_offsets"] = []int{8, 24} },
		func(h map[string]any) { h["unet.attn.lora.up.weight"].(map[string]any)["shape"] = []int{999, 999} },
	} {
		if realLoRA(loraFixture(t, change)) {
			t.Fatal("invalid training artifact reported ready")
		}
	}
}
