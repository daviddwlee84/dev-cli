package hygiene

import (
	"strings"
	"testing"
)

func TestGenericPrivacyNoiseBoundaries(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "127.10.20.30", "0.0.0.0", "::1", "::", "::ffff:127.0.0.1", "::ffff:0.0.0.0"} {
		if !genericPrivacyNoise("privacy-ip", value) {
			t.Errorf("unidentifying IP retained: %s", value)
		}
	}
	for _, value := range []string{"192.168.1.20", "10.2.3.4", "100.64.0.1", "169.254.1.2", "192.0.2.1", "8.8.8.8", "fd00::1", "2001:db8::1", "bad IP"} {
		if genericPrivacyNoise("privacy-ipv6", value) {
			t.Errorf("real or uncertain IP suppressed: %s", value)
		}
	}
	for _, domain := range []string{"service.test", "service.example", "service.invalid", "example.com", "sub.example.net", "EXAMPLE.ORG"} {
		if !genericPrivacyNoise("privacy-email", "user@"+domain) {
			t.Errorf("example email retained: %s", domain)
		}
	}
	for _, domain := range []string{"notexample.com", "example.com.real.net", "example.company", "mytest.com", "service.local"} {
		if genericPrivacyNoise("privacy-email", "user@"+domain) {
			t.Errorf("real email suppressed: %s", domain)
		}
	}
}

func TestGenericPrivacyNoisePreservesKnownRulesAndLineNumbers(t *testing.T) {
	s, r := testService(t)
	s.Policy.Generic = Warn
	s.Policy.Rules = []Rule{
		{ID: "privacy-ip", Kind: "literal", Value: "127.0.0.1", Action: Block},
		{ID: "private-mail", Kind: "literal", Value: "person@example.test", Action: Block},
	}
	body := "127.0.0.1 0.0.0.0 ::1 ::\nperson@example.test\n192.168.1.20\nperson@service.local\n/home/private-person\n"
	put(t, r.Root, "values.txt", body)
	for _, audit := range []bool{false, true} {
		report, err := s.Scan(t.Context(), ScanOptions{Audit: audit})
		if err != nil || report.Blocked != 2 || report.Warnings != 3 {
			t.Fatalf("audit=%v block=%d warn=%d err=%v", audit, report.Blocked, report.Warnings, err)
		}
		for _, f := range report.Findings {
			if f.Category == "generic" && f.Line != map[string]int{"privacy-ip": 3, "privacy-email": 4, "privacy-home-path": 5}[f.Rule] {
				t.Fatalf("line accounting changed: %s at %d", f.Rule, f.Line)
			}
		}
	}
	// Snapshot uses the same native detector, including known-rule precedence.
	record, err := s.scanSnapshot(t.Context(), "values.txt", []byte(strings.TrimSuffix(body, "\n")))
	if err != nil || record.Report.Blocked != 2 || record.Report.Warnings != 3 {
		t.Fatal("snapshot privacy semantics differ")
	}
}
