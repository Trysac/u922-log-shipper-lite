// Command check runs every quality gate of this repository, in order, stopping at the first failure.
//
// Usage: go run ./scripts/check (the same command runs locally, in the factory and in CI);
// go run ./scripts/check semgrep osv runs only the named gates, in the standard order.
//
// Gates: gofmt, go vet, security (gosec, high severity), semgrep (Semgrep CE with the factory's own
// rules in .semgrep/), tests with statement coverage >= 80 % of the library, documentation manifest,
// secrets (gitleaks), known vulnerabilities (govulncheck) and OSV-Scanner over go.mod (high severity).
// Standard library only, no shell: it behaves the same on Windows and Linux.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	minCoverage          = 80.0
	docsManifest         = "docs-manifest.json"
	allowMissingGitleaks = "FABRICA_ALLOW_MISSING_GITLEAKS"
	allowOfflineAudit    = "FABRICA_ALLOW_OFFLINE_AUDIT"
	// ciTier = 1 also builds the files tagged `//go:build conformance` (external tools or
	// downloads); only the CI conformance job sets it, so local gates and coverage skip them.
	ciTier            = "FABRICA_CI"
	govulncheckModule = "golang.org/x/vuln/cmd/govulncheck@latest"
	// Pinned; docker/go-gates.Dockerfile in the factory installs the same version.
	gosecModule = "github.com/securego/gosec/v2/cmd/gosec@v2.30.0"
	// The factory's own Semgrep rules; Semgrep-maintained rules are never used (license).
	semgrepRules  = ".semgrep/factory-rules.yml"
	osvLockfile   = "go.mod"
	highCvss      = 7.0
	osvNoPackages = 128 // osv-scanner's exit code when the lockfile lists no package
)

var gitleaksHint = "gitleaks is not installed. Install it (https://github.com/gitleaks/gitleaks#installing), " +
	"e.g. `winget install Gitleaks.Gitleaks`, `brew install gitleaks` or a release binary, " +
	"or set " + allowMissingGitleaks + "=1 to skip this gate locally."

type gate struct {
	name string
	run  func() error
}

func main() {
	if _, err := os.Stat("go.mod"); err != nil {
		fmt.Fprintln(os.Stderr, "check: run from the repository root (go.mod not found)")
		os.Exit(2)
	}
	gates := []gate{
		{"format", formatGate},
		{"vet", vetGate},
		{"security", securityGate},
		{"semgrep", semgrepGate},
		{"tests", testsGate},
		{"docs", docsGate},
		{"secrets", secretsGate},
		{"vulnerabilities", vulnGate},
		{"osv", osvGate},
	}
	only, known := map[string]bool{}, map[string]bool{}
	for _, g := range gates {
		known[g.name] = true
	}
	for _, name := range os.Args[1:] {
		if !known[name] {
			fmt.Fprintf(os.Stderr, "check: unknown gate %q\n", name)
			os.Exit(2)
		}
		only[name] = true
	}
	for _, g := range gates {
		if len(only) > 0 && !only[g.name] {
			continue
		}
		fmt.Printf("==> %s\n", g.name)
		if err := g.run(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", g.name, err)
			fmt.Fprintf(os.Stderr, "==> %s: FAILED\n", g.name)
			os.Exit(1)
		}
		fmt.Printf("==> %s: ok\n", g.name)
	}
	fmt.Println("==> all gates passed")
}

// run executes a tool from the repository root, streaming its output.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// output executes a tool and returns its trimmed standard output.
func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func goEnv(key string) string {
	value, err := output("go", "env", key)
	if err != nil {
		return ""
	}
	return value
}

func formatGate() error {
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		gofmt = filepath.Join(goEnv("GOROOT"), "bin", "gofmt")
	}
	files, err := output(gofmt, "-l", ".")
	if err != nil {
		return fmt.Errorf("gofmt failed: %w", err)
	}
	if files != "" {
		return fmt.Errorf("files are not gofmt-formatted (run `gofmt -w .`):\n%s", files)
	}
	fmt.Println("gofmt: every file is formatted")
	return nil
}

// tagArgs selects the CI-only conformance files when FABRICA_CI=1.
func tagArgs() []string {
	if os.Getenv(ciTier) == "1" {
		return []string{"-tags=conformance"}
	}
	return nil
}

// vetGate vets the local build and, outside CI, also compiles the conformance-tagged files (without running them),
// so a CI-only test that does not build fails here and not first in CI (L48).
func vetGate() error {
	if err := run("go", append(append([]string{"vet"}, tagArgs()...), "./...")...); err != nil {
		return err
	}
	if os.Getenv(ciTier) == "1" {
		return nil
	}
	fmt.Println("vet: compiling the CI-only conformance tier (-tags=conformance)")
	return run("go", "vet", "-tags=conformance", "./...")
}

// libraryPackages lists every package of the module except the gate tooling under scripts/.
func libraryPackages() ([]string, error) {
	args := append(append([]string{"list"}, tagArgs()...), "-f", "{{.ImportPath}} {{.Dir}}", "./...")
	list, err := output("go", args...)
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}
	scripts, err := filepath.Abs("scripts")
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(list, "\n") {
		importPath, dir, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if rel, err := filepath.Rel(scripts, dir); err == nil && !strings.HasPrefix(rel, "..") {
			continue
		}
		pkgs = append(pkgs, importPath)
	}
	if len(pkgs) == 0 {
		return nil, errors.New("no library packages found")
	}
	return pkgs, nil
}

// raceUnavailable returns why the race detector cannot run here, or "" when it can.
func raceUnavailable() string {
	if goEnv("CGO_ENABLED") != "1" {
		return "cgo is disabled (the race detector needs cgo and a C compiler)"
	}
	cc := strings.Fields(goEnv("CC"))
	if len(cc) == 0 {
		cc = []string{"gcc"}
	}
	if _, err := exec.LookPath(cc[0]); err != nil {
		return fmt.Sprintf("C compiler %q not found on PATH (the race detector needs it)", cc[0])
	}
	return ""
}

func testsGate() error {
	pkgs, err := libraryPackages()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "fabrica-cover-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	profile := filepath.Join(tmp, "cover.out")

	args := append([]string{"test", "-count=1", "-covermode=atomic", "-coverprofile=" + profile}, tagArgs()...)
	if os.Getenv(ciTier) == "1" {
		fmt.Println("tests: CI tier, conformance-tagged files included")
	}
	if reason := raceUnavailable(); reason != "" {
		fmt.Printf("tests: running without -race: %s\n", reason)
	} else {
		args = append(args, "-race")
	}
	if err := run("go", append(args, pkgs...)...); err != nil {
		return fmt.Errorf("go test failed: %w", err)
	}
	report, err := output("go", "tool", "cover", "-func="+profile)
	if err != nil {
		return fmt.Errorf("go tool cover failed: %w", err)
	}
	fmt.Println(report)
	total, err := totalCoverage(report)
	if err != nil {
		return err
	}
	if total < minCoverage {
		return fmt.Errorf("total coverage %.1f%% is below %.0f%%", total, minCoverage)
	}
	fmt.Printf("tests: total coverage %.1f%% (minimum %.0f%%)\n", total, minCoverage)
	return nil
}

// totalCoverage extracts the percentage from the "total:" line of `go tool cover -func`.
func totalCoverage(report string) (float64, error) {
	for _, line := range strings.Split(report, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "total:" {
			return strconv.ParseFloat(strings.TrimSuffix(fields[len(fields)-1], "%"), 64)
		}
	}
	return 0, errors.New("no total line in coverage report")
}

type manifest struct {
	Required      []string          `json:"required"`
	RequiredGlobs []string          `json:"required_globs"`
	MaxLines      map[string]int    `json:"max_lines"`
	MustReference map[string]string `json:"must_reference"`
}

// docsErrors returns every violation of docs-manifest.json; an empty slice means the gate passes.
func docsErrors(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, docsManifest))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", docsManifest, err)
	}
	read := func(rel string) (string, bool) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false
		}
		return string(data), true
	}
	var errs []string
	for _, rel := range m.Required {
		text, ok := read(rel)
		switch {
		case !ok:
			errs = append(errs, "missing required file: "+rel)
		case strings.TrimSpace(text) == "":
			errs = append(errs, "required file is empty: "+rel)
		}
	}
	for _, pattern := range m.RequiredGlobs {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
		}
		found := false
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, "no file matches required pattern: "+pattern)
		}
	}
	for rel, limit := range m.MaxLines {
		if text, ok := read(rel); ok {
			if count := countLines(text); count > limit {
				errs = append(errs, fmt.Sprintf("%s has %d lines (max %d)", rel, count, limit))
			}
		}
	}
	for rel, needle := range m.MustReference {
		if text, ok := read(rel); ok && !strings.Contains(text, needle) {
			errs = append(errs, fmt.Sprintf("%s must reference %s", rel, needle))
		}
	}
	return errs, nil
}

func countLines(text string) int {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(text, "\n"), "\n"))
}

func docsGate() error {
	errs, err := docsErrors(".")
	if err != nil {
		return err
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "docs: %s\n", e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d documentation problem(s)", len(errs))
	}
	fmt.Println("docs: every file in docs-manifest.json is present")
	return nil
}

func secretsGate() error {
	exe, err := exec.LookPath("gitleaks")
	if err != nil {
		if os.Getenv(allowMissingGitleaks) == "1" {
			fmt.Println("gitleaks: SKIPPED (not installed)")
			return nil
		}
		return errors.New("FAIL - " + gitleaksHint)
	}
	return run(exe, "dir", ".", "--redact", "--verbose", "--no-banner")
}

func vulnGate() error {
	if exe, err := exec.LookPath("govulncheck"); err == nil {
		return run(exe, "./...")
	}
	// Probe first, so "cannot download the tool" is told apart from "vulnerabilities found".
	if _, err := output("go", "run", govulncheckModule, "-version"); err != nil {
		if os.Getenv(allowOfflineAudit) == "1" {
			fmt.Println("govulncheck: SKIPPED (not installed and could not be downloaded)")
			return nil
		}
		return fmt.Errorf("govulncheck is not installed and `go run %s` failed (offline?): "+
			"install it with `go install %s` or set %s=1 to skip this gate locally",
			govulncheckModule, govulncheckModule, allowOfflineAudit)
	}
	return run("go", "run", govulncheckModule, "./...")
}

// securityGate scans the library with gosec; only high-severity findings fail. `#nosec` comments are
// ignored (-nosec), so a finding is fixed in the code, never silenced. Tests and scripts/ are not scanned.
func securityGate() error {
	// G115 (integer conversion overflow) is excluded: bit-level reinterpretation is intended in this code base
	// class (interpreters, codecs, hashes) and the rule cannot be silenced per line here (L42).
	args := []string{"-severity", "high", "-nosec", "-quiet", "-exclude=G115", "-exclude-dir=scripts", "./..."}
	if exe, err := exec.LookPath("gosec"); err == nil {
		return run(exe, args...)
	}
	// Probe first, so "cannot download the tool" is told apart from "issues found".
	if _, err := output("go", "run", gosecModule, "-version"); err != nil {
		if os.Getenv(allowOfflineAudit) == "1" {
			fmt.Println("gosec: SKIPPED (not installed and could not be downloaded)")
			return nil
		}
		return fmt.Errorf("gosec is not installed and `go run %s` failed (offline?): "+
			"install it with `go install %s` or set %s=1 to skip this gate locally",
			gosecModule, gosecModule, allowOfflineAudit)
	}
	return run("go", append([]string{"run", gosecModule}, args...)...)
}

// missingScanner handles a scanner that is not installed: skipped locally, a failure in CI (FABRICA_CI=1).
func missingScanner(tool string) error {
	if os.Getenv(ciTier) == "1" {
		return fmt.Errorf("%s not installed (required when %s=1)", tool, ciTier)
	}
	fmt.Printf("%s: skipped: %s not installed\n", tool, tool)
	return nil
}

// semgrepGate runs Semgrep CE with the factory's own rules only; ERROR rules fail and `nosemgrep`
// comments are ignored. Tests and scripts/ are not scanned, like gosec.
func semgrepGate() error {
	exe, err := exec.LookPath("semgrep")
	if err != nil {
		return missingScanner("semgrep")
	}
	return run(exe, "scan", "--config", semgrepRules, "--error", "--severity", "ERROR", "--metrics", "off",
		"--disable-version-check", "--disable-nosem", "--quiet", "--exclude=scripts", "--exclude=*_test.go", ".")
}

type osvReport struct {
	Results []struct {
		Packages []struct {
			Package struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"package"`
			Groups []struct {
				IDs         []string `json:"ids"`
				MaxSeverity string   `json:"max_severity"`
			} `json:"groups"`
		} `json:"packages"`
	} `json:"results"`
}

// osvBlocking lists the advisories with CVSS >= 7.0 or no score (unknown counts as high).
func osvBlocking(report osvReport) []string {
	var out []string
	for _, result := range report.Results {
		for _, pkg := range result.Packages {
			for _, group := range pkg.Groups {
				score, err := strconv.ParseFloat(strings.TrimSpace(group.MaxSeverity), 64)
				if err != nil || score >= highCvss {
					shown := group.MaxSeverity
					if err != nil {
						shown = "unknown"
					}
					out = append(out, fmt.Sprintf("%s@%s %s (%s)", pkg.Package.Name, pkg.Package.Version,
						strings.Join(group.IDs, ","), shown))
				}
			}
		}
	}
	return out
}

// osvGate checks every module in go.mod against OSV; high or unknown severity fails.
func osvGate() error {
	exe, err := exec.LookPath("osv-scanner")
	if err != nil {
		return missingScanner("osv-scanner")
	}
	cmd := exec.Command(exe, "scan", "source", "--format", "json", "--lockfile="+osvLockfile)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		fmt.Println("osv-scanner: no known vulnerabilities in " + osvLockfile)
		return nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == osvNoPackages:
		fmt.Println("osv-scanner: no packages in " + osvLockfile)
		return nil
	case !errors.As(err, &exitErr) || exitErr.ExitCode() != 1:
		return fmt.Errorf("osv-scanner failed: %w", err)
	}
	var report osvReport
	if err := json.Unmarshal(out, &report); err != nil {
		return fmt.Errorf("osv-scanner: unreadable JSON report: %w", err)
	}
	blocking := osvBlocking(report)
	for _, line := range blocking {
		fmt.Fprintln(os.Stderr, "osv-scanner: "+line)
	}
	if len(blocking) > 0 {
		return fmt.Errorf("%d advisory group(s) of high or unknown severity", len(blocking))
	}
	fmt.Println("osv-scanner: only advisories below high severity (reported, not failing)")
	return nil
}
