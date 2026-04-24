package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	helmcli "helm.sh/helm/v4/pkg/cli"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/preview"
)

var expansionError *preview.ExpansionError

var (
	kustomizations  []string
	recursive       bool
	renderHelm      bool
	filtersFile     string
	filterYAML      string
	sortOutput      bool
	excludeCRDs     bool
	verbose         bool
	quiet           bool
	resolveGit      bool
	sopsDecrypt     bool
	configFile      string
	outputFormat    string
	helmRelease     string
	diffSummary     bool
	diffSummaryOnly bool
	initConfig      bool

	helmRegistryConfig   string
	helmRepositoryConfig string
	helmRepositoryCache  string

	// version is set by goreleaser via ldflags.
	version = "dev"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerologr.NameFieldName = "logger"
	zerologr.NameSeparator = "/"

	rootCmd := &cobra.Command{
		Use:   "fmp",
		Short: "Flux Manifest Preview — render and diff Flux GitOps resources",
	}

	rootCmd.PersistentFlags().StringSliceVarP(&kustomizations, "path", "k", nil, "Path to render (kustomize base or directory of YAML, relative to repo root, repeatable)")
	rootCmd.PersistentFlags().BoolVarP(&recursive, "recursive", "r", false, "Recursively discover all paths under each -k directory")
	rootCmd.PersistentFlags().BoolVarP(&renderHelm, "render-helm", "H", true, "Render HelmRelease objects")
	rootCmd.PersistentFlags().StringVar(&filtersFile, "filter", "", "KIO filters definition file (overrides .fmp.yaml filters)")
	rootCmd.PersistentFlags().StringVar(&filterYAML, "filter-yaml", "", "KIO filters YAML string (overrides .fmp.yaml filters)")
	rootCmd.PersistentFlags().BoolVarP(&sortOutput, "sort", "s", false, "Sort output by (kind, namespace, name) for deterministic diffs")
	rootCmd.PersistentFlags().BoolVar(&excludeCRDs, "exclude-crds", false, "Strip CustomResourceDefinitions from output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress informational output (only show errors)")
	rootCmd.PersistentFlags().BoolVar(&resolveGit, "resolve-git", false, "Clone external GitRepository sources to temp dirs")
	rootCmd.PersistentFlags().BoolVar(&sopsDecrypt, "sops-decrypt", false, "Decrypt SOPS-encrypted secrets")
	rootCmd.PersistentFlags().StringVar(&helmRegistryConfig, "registry-config", "", "Helm Registry Config")
	rootCmd.PersistentFlags().StringVar(&helmRepositoryConfig, "repository-config", "", "Helm Repository Config")
	rootCmd.PersistentFlags().StringVar(&helmRepositoryCache, "repository-cache", "", "Helm Repository Cache")

	renderCmd := &cobra.Command{
		Use:   "render <path>",
		Short: "Render a single path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			opts, err := buildOpts(log, args[0])
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			if outputFormat == "json" {
				return p.RenderJSON(args[0], os.Stdout)
			}
			return p.Render(args[0], os.Stdout)
		},
	}

	renderCmd.Flags().StringVarP(&outputFormat, "output", "o", "yaml", "Output format (yaml or json)")
	diffCmd := &cobra.Command{
		Use:   "diff [<rev>|<source-a> <source-b>]",
		Short: "Diff git revisions or directories",
		Long: `Diff rendered manifests from git revisions or filesystem paths.

With no arguments, compares HEAD against the current dirty worktree.
With one argument, compares that git revision against the current worktree.
With two arguments, existing filesystem paths are treated as directory inputs,
and valid git revisions are treated as revision inputs. For mixed or ambiguous
inputs, use explicit git: or path: prefixes.`,
		Example: `  fmp diff
  fmp diff HEAD~1
  fmp diff main feature-branch
  fmp diff ./before ./after
  fmp diff git:HEAD path:/tmp/rendered`,
		Args: validateDiffArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			if outputFormat == "json" {
				return runDiffJSON(log, args, os.Stdout)
			}
			summaryOut, diffOut := diffOutputs(os.Stdout, os.Stderr)
			return runDiff(log, args, summaryOut, diffOut)
		},
	}
	diffCmd.Flags().StringVarP(&outputFormat, "output", "o", "yaml", "Output format (yaml or json)")
	diffCmd.Flags().StringVar(&helmRelease, "hr", "", "Filter diff to a specific HelmRelease by name")
	diffCmd.Flags().BoolVar(&diffSummary, "summary", false, "Print a human-readable change summary to stderr before the raw diff")
	diffCmd.Flags().BoolVar(&diffSummaryOnly, "summary-only", false, "Print only the human-readable summary and suppress the raw diff")
	testCmd := &cobra.Command{
		Use:   "test <path>",
		Short: "Validate all Kustomizations build and HelmReleases render",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			opts, err := buildOpts(log, args[0])
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			if outputFormat == "json" {
				result, err := p.TestJSON(args[0])
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(result); encErr != nil {
					return encErr
				}
				return err
			}
			return p.Test(args[0], os.Stderr)
		},
	}
	testCmd.Flags().StringVarP(&outputFormat, "output", "o", "yaml", "Output format (yaml or json)")

	getCmd := &cobra.Command{
		Use:   "get",
		Short: "List discovered Flux resources",
	}
	getKSCmd := &cobra.Command{
		Use:   "ks <path>",
		Short: "List Flux Kustomizations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			opts, err := buildOpts(log, args[0])
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			ks, err := p.ListKustomizations(args[0])
			if err != nil {
				return err
			}
			if outputFormat == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(preview.KustomizationsToJSON(ks))
			}
			preview.PrintKustomizations(ks, os.Stdout)
			return nil
		},
	}
	getHRCmd := &cobra.Command{
		Use:   "hr <path>",
		Short: "List HelmReleases",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			opts, err := buildOpts(log, args[0])
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			hrs, err := p.ListHelmReleases(args[0])
			if err != nil {
				return err
			}
			if outputFormat == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(preview.HelmReleasesToJSON(hrs))
			}
			preview.PrintHelmReleases(hrs, os.Stdout)
			return nil
		},
	}
	getCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "yaml", "Output format (yaml or json)")
	getCmd.AddCommand(getKSCmd, getHRCmd)

	ciCmd := &cobra.Command{
		Use:   "ci",
		Short: "Run CI diff using environment variables",
		Long: `Run CI diff using environment variables.
Auto-discovers .fmp.yaml from FMP_REPO_A (base branch).
FMP_CONFIG can point to an explicit config file (overrides auto-discovery).
CLI flags and FMP_* env vars override the config file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			repoA := os.Getenv("FMP_REPO_A")
			repoB := os.Getenv("FMP_REPO_B")
			if repoA == "" || repoB == "" {
				return fmt.Errorf("must set both FMP_REPO_A and FMP_REPO_B")
			}

			if ks := os.Getenv("FMP_KUSTOMIZATIONS"); ks != "" {
				kustomizations = parseLines(ks)
			}
			if v := os.Getenv("FMP_RENDER_HELM"); v != "" {
				renderHelm = v == "true" || v == "1"
			}

			configRepo := repoA
			if explicitConfig := os.Getenv("FMP_CONFIG"); explicitConfig != "" {
				configFile = explicitConfig
				configRepo = ""
			}

			opts, err := buildOpts(log, configRepo)
			if err != nil {
				return err
			}

			if v := os.Getenv("FMP_FILTER"); v != "" {
				opts = append(opts, preview.WithFilterYAML(v))
			}

			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			return p.Diff(repoA, repoB, os.Stdout)
		},
	}
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print fmp version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("fmp %s\n", version)
		},
	}

	detectCmd := &cobra.Command{
		Use:   "detect-permadiffs <path>",
		Short: "Detect non-deterministic output and generate normalization filter config",
		Long: `Renders the same path twice and compares the results.
Any resource that differs between two renders of the same code is
non-deterministic (e.g. auto-generated TLS keys). Outputs a filter
config YAML that can be used with --filter to normalize these fields.

Use --init to generate a complete .fmp.yaml config file in the repo.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := cliLogger()
			if initConfig {
				return generateInitConfig(args[0])
			}
			opts, err := buildOptsNoFilters(log, args[0])
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			return p.DetectPermadiffs(args[0], os.Stdout)
		},
	}
	detectCmd.Flags().BoolVar(&initConfig, "init", false, "Generate a .fmp.yaml config file in the repo root")

	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.AddCommand(renderCmd, diffCmd, testCmd, getCmd, ciCmd, detectCmd, versionCmd, githubActionCmd())

	if err := rootCmd.Execute(); err != nil {
		code := exitCodeFor(err)
		if outputFormat == "json" {
			writeJSONError(os.Stdout, err)
			os.Exit(code)
		}
		if errors.As(err, &expansionError) {
			for _, w := range expansionError.Warnings {
				fmt.Fprintf(os.Stderr, "WARNING: %v\n", w)
			}
			for _, e := range expansionError.Errors {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", e)
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(code)
	}
}

// Semantic exit codes for agent-friendly error handling.
//   0 = success
//   1 = differences found (diff only) or generic error
//   2 = user input error (bad args, missing file, invalid config)
//   3 = dependency failure (Helm chart missing, git error, network)
//   5 = policy violation
//   10 = unexpected internal error
var (
	ErrUserInput       = errors.New("user input error")
	ErrDependency      = errors.New("dependency failure")
	ErrPolicyViolation = errors.New("policy violation")
)

func userError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUserInput, fmt.Sprintf(format, args...))
}

func depError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDependency, fmt.Sprintf(format, args...))
}

func exitCodeFor(err error) int {
	if errors.Is(err, ErrUserInput) {
		return 2
	}
	if errors.Is(err, ErrDependency) {
		return 3
	}
	if errors.Is(err, ErrPolicyViolation) {
		return 5
	}
	// Expansion errors are treated as dependency failures if they contain
	// render/Helm errors, otherwise generic.
	if errors.As(err, &expansionError) {
		return 3
	}
	return 1
}

// jsonErrorEnvelope is the structured error output used when --output json is set.
type jsonErrorEnvelope struct {
	Status string    `json:"status"`
	Data   interface{} `json:"data"`
	Error  jsonError `json:"error"`
}

type jsonError struct {
	Reason  string                 `json:"reason"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

func writeJSONError(out io.Writer, err error) {
	env := jsonErrorEnvelope{
		Status: "failure",
		Data:   nil,
		Error: jsonError{
			Reason:  "CommandFailed",
			Message: err.Error(),
		},
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
}

func cliLogger() logr.Logger {
	level := zerolog.WarnLevel
	if verbose {
		level = zerolog.DebugLevel
	}
	if quiet {
		level = zerolog.ErrorLevel
	}

	zl := zerolog.New(os.Stderr).Level(level)
	zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
	return zerologr.New(&zl)
}

func buildOpts(log logr.Logger, configRepoPath string) ([]preview.Opt, error) {
	return buildOptsWithFilters(log, configRepoPath, true)
}

func buildOptsNoFilters(log logr.Logger, configRepoPath string) ([]preview.Opt, error) {
	return buildOptsWithFilters(log, configRepoPath, false)
}

func buildOptsWithFilters(log logr.Logger, configRepoPath string, applyFilters bool) ([]preview.Opt, error) {
	cfg, err := loadConfigForRepo(configRepoPath, configFile)
	if err != nil {
		if configFile != "" {
			return nil, fmt.Errorf("loading config %s: %w", configFile, err)
		}
		return nil, fmt.Errorf("loading config: %w", err)
	}

	paths := kustomizations
	doRecursive := recursive
	doHelm := renderHelm
	doResolveGit := resolveGit
	doSort := sortOutput
	doExcludeCRDs := excludeCRDs
	doSOPSDecrypt := sopsDecrypt

	if cfg != nil {
		if len(paths) == 0 && len(cfg.Paths) > 0 {
			paths = cfg.Paths
		}
		if !cmdChanged("recursive") {
			doRecursive = config.BoolOr(cfg.Recursive, doRecursive)
		}
		if !cmdChanged("render-helm") {
			doHelm = config.BoolOr(cfg.Helm, doHelm)
		}
		if !cmdChanged("resolve-git") {
			doResolveGit = config.BoolOr(cfg.ResolveGit, doResolveGit)
		}
		if !cmdChanged("sort") {
			doSort = config.BoolOr(cfg.Sort, doSort)
		}
		if !cmdChanged("exclude-crds") {
			doExcludeCRDs = config.BoolOr(cfg.ExcludeCRDs, doExcludeCRDs)
		}
		if !cmdChanged("sops-decrypt") {
			doSOPSDecrypt = config.BoolOr(cfg.SOPSDecrypt, doSOPSDecrypt)
		}
		if cfg.HelmSettings != nil {
			if helmRegistryConfig == "" {
				helmRegistryConfig = cfg.HelmSettings.RegistryConfig
			}
			if helmRepositoryConfig == "" {
				helmRepositoryConfig = cfg.HelmSettings.RepositoryConfig
			}
			if helmRepositoryCache == "" {
				helmRepositoryCache = cfg.HelmSettings.RepositoryCache
			}
		}
	}

	opts := []preview.Opt{
		preview.WithLogger(log),
		preview.WithPaths(paths, doRecursive),
	}

	if doResolveGit {
		opts = append(opts, preview.WithGitRepo())
	}

	opts = append(opts, preview.WithFluxKS())

	if doHelm {
		opts = append(opts, preview.WithHelm(helmSettings()))
	}

	if doSort {
		opts = append(opts, preview.WithSort())
	}

	if doExcludeCRDs {
		opts = append(opts, preview.WithExcludeCRDs())
	}

	if doSOPSDecrypt {
		opts = append(opts, preview.WithSOPSDecrypt())
	}

	if applyFilters {
		if filtersFile != "" {
			f, err := os.Open(filtersFile)
			if err != nil {
				return nil, fmt.Errorf("opening filter file: %w", err)
			}
			defer func() { _ = f.Close() }()
			opts = append(opts, preview.WithFilterFile(f))
		} else if filterYAML != "" {
			opts = append(opts, preview.WithFilterYAML(filterYAML))
		} else if cfg != nil && len(cfg.Filters.Filters) > 0 {
			opts = append(opts, preview.WithFilterConfig(&cfg.Filters))
		}
	}

	return opts, nil
}

func cmdChanged(flagName string) bool {
	for _, cmd := range os.Args[1:] {
		for _, name := range []string{"--" + flagName, "-" + shortFlag(flagName)} {
			if cmd == name || strings.HasPrefix(cmd, name+"=") {
				return true
			}
		}
	}
	return false
}

func shortFlag(name string) string {
	switch name {
	case "recursive":
		return "r"
	case "render-helm":
		return "H"
	case "sort":
		return "s"
	case "quiet":
		return "q"
	default:
		return ""
	}
}

func generateInitConfig(repoPath string) error {
	dest := config.DiscoverConfigPath(repoPath)
	if dest != "" {
		return fmt.Errorf("config file already exists at %s (remove it first or edit manually)", dest)
	}

	dest = repoPath + "/.fmp.yaml"

	opts, err := buildOpts(logr.Discard(), repoPath)
	if err != nil {
		return err
	}

	p, err := preview.New(opts...)
	if err != nil {
		return fmt.Errorf("error creating preview: %w", err)
	}

	if err := p.GenerateInitConfig(repoPath, dest); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Generated %s\n", dest)
	return nil
}

func helmSettings() *helmcli.EnvSettings {
	settings := helmcli.New()
	if helmRepositoryConfig != "" {
		settings.RepositoryConfig = helmRepositoryConfig
	}
	if helmRegistryConfig != "" {
		settings.RegistryConfig = helmRegistryConfig
	}
	if helmRepositoryCache != "" {
		settings.RepositoryCache = helmRepositoryCache
	}
	return settings
}

func parseLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func diffOutputs(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if diffSummaryOnly {
		return stdout, io.Discard
	}
	if diffSummary {
		return stderr, stdout
	}
	return io.Discard, stdout
}
