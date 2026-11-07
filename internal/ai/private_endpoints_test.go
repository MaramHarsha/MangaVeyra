package ai

import (
	"context"
	"testing"
)

func TestPrivateEndpointPolicyDefaultsToEnvironmentAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	keys := &fakeKeys{}
	p := &privateEndpointPolicy{keys: keys, fallback: true}
	if !p.Allowed(ctx) {
		t.Fatal("unset setting must follow MV_ALLOW_PRIVATE_PROVIDER_URLS")
	}
	if err := p.Set(ctx, false, "admin"); err != nil || p.Allowed(ctx) {
		t.Fatal("stored choice must override the environment default")
	}
	keys.err = context.DeadlineExceeded
	if (&privateEndpointPolicy{keys: keys, fallback: true}).Allowed(ctx) {
		t.Fatal("a settings read failure must fail closed")
	}
	if (*privateEndpointPolicy)(nil).Allowed(ctx) {
		t.Fatal("nil policy must refuse")
	}
	for raw, blocked := range map[string]bool{"http://169.254.169.254/v1": true, "http://[fe80::1]/v1": true, "http://127.0.0.1:11434/v1": false, "http://localhost:11434/v1": false, "http://10.0.0.4/v1": false} {
		if endpointNeverAllowed(raw) != blocked {
			t.Errorf("endpointNeverAllowed(%s) != %v", raw, blocked)
		}
	}
}

type fakeKeys struct {
	value *privateEndpointsSetting
	err   error
}

func (f *fakeKeys) GetKey(_ context.Context, _ string, dest any) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.value == nil {
		return false, nil
	}
	*(dest.(*privateEndpointsSetting)) = *f.value
	return true, nil
}

func (f *fakeKeys) SetKey(_ context.Context, _ string, v any, _ string) error {
	setting := v.(privateEndpointsSetting)
	f.value = &setting
	return nil
}
