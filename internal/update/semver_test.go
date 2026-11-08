package update

import "testing"

func mustSemver(t *testing.T, s string) Semver {
	t.Helper()
	v, err := ParseSemver(s)
	if err != nil {
		t.Fatalf("ParseSemver(%q): %v", s, err)
	}
	return v
}

func TestParseSemver(t *testing.T) {
	for _, ok := range []string{"1.0.0", "v1.2.3", "10.20.30", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-rc.1+build.5", "1.0.0+20260101"} {
		if _, err := ParseSemver(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1", "1.0", "1.0.0.0", "01.0.0", "1.02.0", "1.0.0-", "1.0.0-01", "1.0.0-beta..1", "a.b.c", "1.0.0+", "dev", "1.0.0 "} {
		if _, err := ParseSemver(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if s := mustSemver(t, "v1.2.3-beta.1+x").String(); s != "1.2.3-beta.1" {
		t.Fatalf("String = %q", s)
	}
}

func TestSemverPrecedence(t *testing.T) {
	// semver.org §11 example order.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := 0; i+1 < len(order); i++ {
		a, b := mustSemver(t, order[i]), mustSemver(t, order[i+1])
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should sort before %s", order[i], order[i+1])
		}
	}
	if mustSemver(t, "1.0.0+a").Compare(mustSemver(t, "1.0.0+b")) != 0 {
		t.Fatal("build metadata must not change precedence")
	}
}

func TestClassifyAndEligible(t *testing.T) {
	cur := mustSemver(t, "1.0.0")
	cases := []struct{ next, kind string }{{"1.0.2", KindPatch}, {"1.1.0", KindMinor}, {"2.0.0", KindMajor}, {"1.0.1-beta.1", KindPatch}}
	for _, c := range cases {
		if got := Classify(cur, mustSemver(t, c.next)); got != c.kind {
			t.Errorf("Classify(1.0.0, %s) = %s, want %s", c.next, got, c.kind)
		}
	}
	if !Eligible(cur, mustSemver(t, "1.0.1"), ChannelStable) {
		t.Error("a newer release must be offered on stable")
	}
	if Eligible(cur, mustSemver(t, "1.0.1-beta.1"), ChannelStable) {
		t.Error("stable must never offer a pre-release")
	}
	if !Eligible(cur, mustSemver(t, "1.0.1-beta.1"), ChannelBeta) {
		t.Error("beta offers pre-releases")
	}
	if Eligible(cur, cur, ChannelBeta) || Eligible(cur, mustSemver(t, "0.9.9"), ChannelStable) {
		t.Error("the same or an older version is never offered")
	}
	if Eligible(mustSemver(t, "1.0.0"), mustSemver(t, "1.0.0-rc.1"), ChannelBeta) {
		t.Error("a pre-release of the running version is older")
	}
}
