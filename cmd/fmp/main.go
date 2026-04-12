package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	helmcli "helm.sh/helm/v4/pkg/cli"

	"github.com/tobiash/flux-manifest-preview/pkg/preview"
)

var (
	kustomizations []string
	recursive      bool
	renderHelm     bool
	filtersFile    string
	filterYAML     string
	sortOutput     bool
	excludeCRDs    bool
	quiet          bool
	resolveGit     bool

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

	zl := zerolog.New(os.Stderr)
	zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
	var log logr.Logger = zerologr.New(&zl)

	rootCmd := &cobra.Command{
		Use:   "fmp",
		Short: "Flux Manifest Preview — render and diff Flux GitOps resources",
	}

	rootCmd.PersistentFlags().StringSliceVarP(&kustomizations, "path", "k", nil, "Path to render (kustomize base or directory of YAML, relative to repo root, repeatable)")
	rootCmd.PersistentFlags().BoolVarP(&recursive, "recursive", "r", false, "Recursively discover all paths under each -k directory")
	rootCmd.PersistentFlags().BoolVarP(&renderHelm, "render-helm", "H", true, "Render HelmRelease objects")
	rootCmd.PersistentFlags().StringVar(&filtersFile, "filter", "", "KIO filters definition file")
	rootCmd.PersistentFlags().StringVar(&filterYAML, "filter-yaml", "", "KIO filters YAML string")
	rootCmd.PersistentFlags().BoolVarP(&sortOutput, "sort", "s", false, "Sort output by (kind, namespace, name) for deterministic diffs")
	rootCmd.PersistentFlags().BoolVar(&excludeCRDs, "exclude-crds", false, "Strip CustomResourceDefinitions from output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress informational output (only show errors)")
	rootCmd.PersistentFlags().BoolVar(&resolveGit, "resolve-git", false, "Clone external GitRepository sources to temp dirs")
	rootCmd.PersistentFlags().StringVar(&helmRegistryConfig, "registry-config", "", "Helm Registry Config")
	rootCmd.PersistentFlags().StringVar(&helmRepositoryConfig, "repository-config", "", "Helm Repository Config")
	rootCmd.PersistentFlags().StringVar(&helmRepositoryCache, "repository-cache", "", "Helm Repository Cache")

	renderCmd := &cobra.Command{
		Use:   "render <path>",
		Short: "Render a single path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts(log)
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			return p.Render(args[0], os.Stdout)
		},
	}
	diffCmd := &cobra.Command{
		Use:   "diff <path-a> <path-b>",
		Short: "Diff two paths",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts(log)
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			return p.Diff(args[0], args[1], os.Stdout)
		},
	}
	testCmd := &cobra.Command{
		Use:   "test <path>",
		Short: "Validate all Kustomizations build and HelmReleases render",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts(log)
			if err != nil {
				return err
			}
			p, err := preview.New(opts...)
			if err != nil {
				return fmt.Errorf("error creating preview: %w", err)
			}
			return p.Test(args[0], os.Stderr)
		},
	}

	getCmd := &cobra.Command{
		Use:   "get",
		Short: "List discovered Flux resources",
	}
	getKSCmd := &cobra.Command{
		Use:   "ks <path>",
		Short: "List Flux Kustomizations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts(log)
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
			preview.PrintKustomizations(ks, os.Stdout)
			return nil
		},
	}
	getHRCmd := &cobra.Command{
		Use:   "hr <path>",
		Short: "List HelmReleases",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOpts(log)
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
			preview.PrintHelmReleases(hrs, os.Stdout)
			return nil
		},
	}
	getCmd.AddCommand(getKSCmd, getHRCmd)

	ciCmd := &cobra.Command{
		Use:   "ci",
		Short: "Run CI diff using environment variables",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoA := os.Getenv("FMP_REPO_A")
			repoB := os.Getenv("FMP_REPO_B")
			if repoA == "" || repoB == "" {
				return fmt.Errorf("must set both FMP_REPO_A and FMP_REPO_B")
			}

			// Override flags from env vars before building options.
			if ks := os.Getenv("FMP_KUSTOMIZATIONS"); ks != "" {
				kustomizations = parseLines(ks)
			}
			if v := os.Getenv("FMP_RENDER_HELM"); v != "" {
				renderHelm = v == "true" || v == "1"
			}

			opts, err := buildOpts(log)
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

	rootCmd.AddCommand(renderCmd, diffCmd, testCmd, getCmd, ciCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func buildOpts(log logr.Logger) ([]preview.Opt, error) {
	// In quiet mode, suppress info-level logs.
	if quiet {
		zl := zerolog.New(os.Stderr)
		zl = zl.With().Caller().Timestamp().Logger().Output(zerolog.ConsoleWriter{Out: os.Stderr})
		zl = zl.Level(zerolog.ErrorLevel)
		log = zerologr.New(&zl)
	}

	opts := []preview.Opt{
		preview.WithLogger(log),
		preview.WithPaths(kustomizations, recursive),
	}

	if resolveGit {
		opts = append(opts, preview.WithGitRepo())
	}

	opts = append(opts, preview.WithFluxKS())

	if renderHelm {
		opts = append(opts, preview.WithHelm(helmSettings()))
	}

	if sortOutput {
		opts = append(opts, preview.WithSort())
	}

	if excludeCRDs {
		opts = append(opts, preview.WithExcludeCRDs())
	}

	if filtersFile != "" {
		f, err := os.Open(filtersFile)
		if err != nil {
			return nil, fmt.Errorf("opening filter file: %w", err)
		}
		defer f.Close()
		opts = append(opts, preview.WithFilterFile(f))
	} else if filterYAML != "" {
		opts = append(opts, preview.WithFilterYAML(filterYAML))
	}

	return opts, nil
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
