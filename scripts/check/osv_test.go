package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The reports are trimmed from a real OSV-Scanner v2.6.0 run (`osv-scanner scan source --format json`) over a
// lockfile with minimist 1.2.0 and lodash 4.17.15. The unscored group keeps the recorded shape with an empty
// max_severity, which is what OSV-Scanner writes when an advisory has no CVSS score.
const (
	reportHighCvss = `{"results": [{"source": {"path": "/work/go.mod", "type": "lockfile"}, "packages": [{
  "package": {"name": "minimist", "version": "1.2.0", "ecosystem": "npm"},
  "groups": [
    {"ids": ["GHSA-vh95-rmgr-6w4m"], "aliases": ["CVE-2020-7598", "GHSA-vh95-rmgr-6w4m"], "max_severity": "5.6"},
    {"ids": ["GHSA-xvch-5gv4-984h"], "aliases": ["CVE-2021-44906", "GHSA-xvch-5gv4-984h"], "max_severity": "9.8"}
  ],
  "vulnerabilities": [{"id": "GHSA-vh95-rmgr-6w4m"}, {"id": "GHSA-xvch-5gv4-984h"}]}]}],
  "experimental_config": {"licenses": {"summary": false, "allowlist": null}}}`
	reportBelowHigh = `{"results": [{"source": {"path": "/work/go.mod", "type": "lockfile"}, "packages": [{
  "package": {"name": "lodash", "version": "4.17.15", "ecosystem": "npm"},
  "groups": [
    {"ids": ["GHSA-29mw-wpgm-hmr9"], "aliases": ["CVE-2020-28500", "GHSA-29mw-wpgm-hmr9"], "max_severity": "5.3"},
    {"ids": ["GHSA-xxjr-mmjv-4gpg"], "aliases": ["CVE-2025-13465", "GHSA-xxjr-mmjv-4gpg"], "max_severity": "6.9"}
  ],
  "vulnerabilities": [{"id": "GHSA-29mw-wpgm-hmr9"}, {"id": "GHSA-xxjr-mmjv-4gpg"}]}]}],
  "experimental_config": {"licenses": {"summary": false, "allowlist": null}}}`
	reportNoScore = `{"results": [{"source": {"path": "/work/go.mod", "type": "lockfile"}, "packages": [{
  "package": {"name": "lodash", "version": "4.17.15", "ecosystem": "npm"},
  "groups": [{"ids": ["GHSA-29mw-wpgm-hmr9"], "aliases": ["GHSA-29mw-wpgm-hmr9"], "max_severity": ""}],
  "vulnerabilities": [{"id": "GHSA-29mw-wpgm-hmr9"}]}]}],
  "experimental_config": {"licenses": {"summary": false, "allowlist": null}}}`
)

func TestOsvBlocking(t *testing.T) {
	cases := []struct {
		name   string
		report string
		want   []string
	}{
		{"high_cvss", reportHighCvss, []string{"minimist@1.2.0 GHSA-xvch-5gv4-984h (9.8)"}},
		{"below_high", reportBelowHigh, nil},
		{"no_score", reportNoScore, []string{"lodash@4.17.15 GHSA-29mw-wpgm-hmr9 (unknown)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var report osvReport
			if err := json.Unmarshal([]byte(tc.report), &report); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := osvBlocking(report); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("osvBlocking() = %q, want %q", got, tc.want)
			}
		})
	}
}
