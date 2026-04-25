package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
	gitrepoexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/gitrepo"
	"github.com/tobiash/flux-manifest-preview/pkg/policy"
	"github.com/tobiash/flux-manifest-preview/pkg/preview"
)

type diffSourceKind string

const (
	diffSourceAuto     diffSourceKind = ""
	diffSourcePath     diffSourceKind = "path"
	diffSourceRevision diffSourceKind = "revision"
	diffSourceWorktree diffSourceKind = "worktree"
)

type diffSource struct {
	kind     diffSourceKind
	raw      string
	repoRoot string
}

type diffPlan struct {
	configRoot string
	left       diffSource
	right      diffSource
}

func validateDiffArgs(_ *cobra.Command, args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("accepts at most 2 arg(s), received %d", len(args))
	}
	return nil
}

func runDiff(log logr.Logger, args []string, summaryOut io.Writer, diffOut io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determining current directory: %w", err)
	}

	plan, err := resolveDiffPlan(args, cwd)
	if err != nil {
		return err
	}

	leftPath, leftCleanup, err := plan.left.materialize(context.Background())
	if err != nil {
		return err
	}
	if leftCleanup != nil {
		defer leftCleanup()
	}

	rightPath, rightCleanup, err := plan.right.materialize(context.Background())
	if err != nil {
		return err
	}
	if rightCleanup != nil {
		defer rightCleanup()
	}

	opts, err := buildOpts(log, plan.configRoot)
	if err != nil {
		return err
	}
	if helmRelease != "" {
		opts = append(opts, preview.WithHelmReleaseFilter(helmRelease))
	}

	p, err := preview.New(opts...)
	if err != nil {
		return fmt.Errorf("error creating preview: %w", err)
	}

	var diffText bytes.Buffer
	result, err := p.DiffResult(context.Background(), leftPath, rightPath, &diffText)
	if err != nil {
		return err
	}

	cfg, err := loadConfigForRepo(plan.configRoot, configFile)
	if err != nil {
		if configFile != "" {
			return fmt.Errorf("loading config %s: %w", configFile, err)
		}
		return fmt.Errorf("loading config: %w", err)
	}

	var policyCfg *config.PolicyConfig
	if cfg != nil {
		policyCfg = cfg.Policies
	}
	policyResult, err := policy.Evaluate(context.Background(), result, policyCfg, policyBaseDir(plan.configRoot, cfg))
	if err != nil {
		return fmt.Errorf("evaluating policies: %w", err)
	}

	if err := writeDiffSummary(summaryOut, result, policyResult); err != nil {
		return fmt.Errorf("writing diff summary: %w", err)
	}
	if _, err = io.Copy(diffOut, &diffText); err != nil {
		return err
	}
	if policyResult.PolicyFailed {
		return fmt.Errorf("%w: %s", ErrPolicyViolation, strings.Join(policyResult.PolicyFailures, ", "))
	}
	return nil
}

func runDiffJSON(log logr.Logger, args []string, out io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determining current directory: %w", err)
	}

	plan, err := resolveDiffPlan(args, cwd)
	if err != nil {
		return err
	}

	leftPath, leftCleanup, err := plan.left.materialize(context.Background())
	if err != nil {
		return err
	}
	if leftCleanup != nil {
		defer leftCleanup()
	}

	rightPath, rightCleanup, err := plan.right.materialize(context.Background())
	if err != nil {
		return err
	}
	if rightCleanup != nil {
		defer rightCleanup()
	}

	opts, err := buildOpts(log, plan.configRoot)
	if err != nil {
		return err
	}
	if helmRelease != "" {
		opts = append(opts, preview.WithHelmReleaseFilter(helmRelease))
	}

	p, err := preview.New(opts...)
	if err != nil {
		return fmt.Errorf("error creating preview: %w", err)
	}

	var diffText bytes.Buffer
	result, err := p.DiffResult(context.Background(), leftPath, rightPath, &diffText)
	if err != nil {
		return err
	}

	cfg, err := loadConfigForRepo(plan.configRoot, configFile)
	if err != nil {
		if configFile != "" {
			return fmt.Errorf("loading config %s: %w", configFile, err)
		}
		return fmt.Errorf("loading config: %w", err)
	}

	var policyCfg *config.PolicyConfig
	if cfg != nil {
		policyCfg = cfg.Policies
	}
	policyResult, err := policy.Evaluate(context.Background(), result, policyCfg, policyBaseDir(plan.configRoot, cfg))
	if err != nil {
		return fmt.Errorf("evaluating policies: %w", err)
	}

	jsonResult := result.ToJSON()
	output := map[string]any{
		"added":    jsonResult.Added,
		"deleted":  jsonResult.Deleted,
		"modified": jsonResult.Modified,
		"policy":   policyResult,
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	if policyResult.PolicyFailed {
		return fmt.Errorf("%w: %s", ErrPolicyViolation, strings.Join(policyResult.PolicyFailures, ", "))
	}
	return nil
}

func resolveDiffPlan(args []string, cwd string) (*diffPlan, error) {
	switch len(args) {
	case 0:
		repoRoot, err := gitRepoRoot(cwd)
		if err != nil {
			return nil, fmt.Errorf("default diff requires a git worktree: %w", err)
		}
		return &diffPlan{
			configRoot: repoRoot,
			left:       diffSource{kind: diffSourceRevision, raw: "HEAD", repoRoot: repoRoot},
			right:      diffSource{kind: diffSourceWorktree, raw: repoRoot, repoRoot: repoRoot},
		}, nil
	case 1:
		repoRoot, err := gitRepoRoot(cwd)
		if err != nil {
			return nil, fmt.Errorf("single-argument diff requires a git worktree: %w", err)
		}
		left, err := resolveSingleRevisionArg(args[0], repoRoot)
		if err != nil {
			return nil, err
		}
		return &diffPlan{
			configRoot: repoRoot,
			left:       left,
			right:      diffSource{kind: diffSourceWorktree, raw: repoRoot, repoRoot: repoRoot},
		}, nil
	case 2:
		return resolveTwoDiffSources(args[0], args[1], cwd)
	default:
		return nil, fmt.Errorf("accepts at most 2 arg(s), received %d", len(args))
	}
}

func resolveSingleRevisionArg(arg, repoRoot string) (diffSource, error) {
	kind, value := splitDiffArg(arg)
	if kind == diffSourcePath {
		return diffSource{}, fmt.Errorf("single diff argument must be a git revision; use two paths for directory diffs")
	}
	if !gitRevisionExists(repoRoot, value) {
		return diffSource{}, fmt.Errorf("%q is not a valid git revision; use two paths for directory diffs", arg)
	}
	return diffSource{kind: diffSourceRevision, raw: value, repoRoot: repoRoot}, nil
}

func resolveTwoDiffSources(leftArg, rightArg, cwd string) (*diffPlan, error) {
	leftKind, leftValue := splitDiffArg(leftArg)
	rightKind, rightValue := splitDiffArg(rightArg)
	leftExplicit := leftKind != diffSourceAuto
	rightExplicit := rightKind != diffSourceAuto

	leftPath, leftPathOK := existingPathCandidate(leftKind, leftValue, cwd)
	rightPath, rightPathOK := existingPathCandidate(rightKind, rightValue, cwd)
	if leftKind != diffSourceRevision && rightKind != diffSourceRevision && leftPathOK && rightPathOK {
		return &diffPlan{
			configRoot: leftPath,
			left:       diffSource{kind: diffSourcePath, raw: leftPath},
			right:      diffSource{kind: diffSourcePath, raw: rightPath},
		}, nil
	}

	repoRoot, err := gitRepoRoot(cwd)
	if err != nil {
		if leftKind == diffSourceRevision || rightKind == diffSourceRevision {
			return nil, fmt.Errorf("git revision diff requires a git worktree: %w", err)
		}
	} else {
		leftRev, leftRevOK := revisionCandidate(leftKind, leftValue, repoRoot)
		rightRev, rightRevOK := revisionCandidate(rightKind, rightValue, repoRoot)
		if leftKind != diffSourcePath && rightKind != diffSourcePath && leftRevOK && rightRevOK {
			return &diffPlan{
				configRoot: repoRoot,
				left:       diffSource{kind: diffSourceRevision, raw: leftRev, repoRoot: repoRoot},
				right:      diffSource{kind: diffSourceRevision, raw: rightRev, repoRoot: repoRoot},
			}, nil
		}
	}

	if !leftExplicit && !rightExplicit {
		return nil, fmt.Errorf("mixed or ambiguous diff inputs require explicit git: or path: prefixes")
	}

	left, err := resolveExplicitOrUnambiguousSource(leftArg, repoRoot, cwd)
	if err != nil {
		return nil, err
	}
	right, err := resolveExplicitOrUnambiguousSource(rightArg, repoRoot, cwd)
	if err != nil {
		return nil, err
	}

	configRoot := left.raw
	if left.kind == diffSourceRevision || left.kind == diffSourceWorktree || right.kind == diffSourceRevision || right.kind == diffSourceWorktree {
		if repoRoot == "" {
			return nil, fmt.Errorf("git revision diff requires a git worktree")
		}
		configRoot = repoRoot
	}

	return &diffPlan{configRoot: configRoot, left: left, right: right}, nil
}

func resolveExplicitOrUnambiguousSource(arg, repoRoot, cwd string) (diffSource, error) {
	kind, value := splitDiffArg(arg)
	switch kind {
	case diffSourcePath:
		path, ok := existingPathCandidate(kind, value, cwd)
		if !ok {
			return diffSource{}, fmt.Errorf("path %q does not exist", value)
		}
		return diffSource{kind: diffSourcePath, raw: path}, nil
	case diffSourceRevision:
		if repoRoot == "" {
			return diffSource{}, fmt.Errorf("git revision %q requires a git worktree", value)
		}
		rev, ok := revisionCandidate(kind, value, repoRoot)
		if !ok {
			return diffSource{}, fmt.Errorf("%q is not a valid git revision", value)
		}
		return diffSource{kind: diffSourceRevision, raw: rev, repoRoot: repoRoot}, nil
	default:
		path, pathOK := existingPathCandidate(kind, value, cwd)
		rev, revOK := revisionCandidate(kind, value, repoRoot)
		switch {
		case pathOK && !revOK:
			return diffSource{kind: diffSourcePath, raw: path}, nil
		case revOK && !pathOK:
			return diffSource{kind: diffSourceRevision, raw: rev, repoRoot: repoRoot}, nil
		case pathOK && revOK:
			return diffSource{}, fmt.Errorf("%q is ambiguous: it is both an existing path and a git revision; use path:%s or git:%s", arg, value, value)
		default:
			return diffSource{}, fmt.Errorf("%q is neither an existing path nor a valid git revision", arg)
		}
	}
}

func splitDiffArg(arg string) (diffSourceKind, string) {
	switch {
	case strings.HasPrefix(arg, "git:"):
		return diffSourceRevision, strings.TrimPrefix(arg, "git:")
	case strings.HasPrefix(arg, "path:"):
		return diffSourcePath, strings.TrimPrefix(arg, "path:")
	default:
		return diffSourceAuto, arg
	}
}

func existingPathCandidate(kind diffSourceKind, value, cwd string) (string, bool) {
	if kind == diffSourceRevision {
		return "", false
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

func revisionCandidate(kind diffSourceKind, value, repoRoot string) (string, bool) {
	if repoRoot == "" || kind == diffSourcePath {
		return "", false
	}
	if !gitRevisionExists(repoRoot, value) {
		return "", false
	}
	return value, true
}

func (s diffSource) materialize(ctx context.Context) (string, func(), error) {
	switch s.kind {
	case diffSourcePath:
		return s.raw, nil, nil
	case diffSourceWorktree:
		return s.repoRoot, nil, nil
	case diffSourceRevision:
		return materializeRevision(ctx, s.repoRoot, s.raw)
	default:
		return "", nil, fmt.Errorf("unsupported diff source kind %q", s.kind)
	}
}

func gitRepoRoot(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolving git repo root: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRevisionExists(repoRoot, rev string) bool {
	if rev == "" {
		return false
	}
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "--quiet", rev+"^{tree}")
	return cmd.Run() == nil
}

func materializeRevision(ctx context.Context, repoRoot, rev string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "fmp-diff-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for %s: %w", rev, err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "archive", "--format=tar", rev)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("creating archive pipe for %s: %w", rev, err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("starting git archive %s: %w", rev, err)
	}
	if err := extractTar(tmpDir, stdout); err != nil {
		_ = cmd.Wait()
		cleanup()
		return "", nil, fmt.Errorf("extracting git archive %s: %w", rev, err)
	}
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git archive %s: %s: %w", rev, strings.TrimSpace(stderr.String()), err)
	}
	if err := gitrepoexpander.WriteSourceRepoURLs(tmpDir, repoRoot); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing source repo metadata for %s: %w", rev, err)
	}
	return tmpDir, cleanup, nil
}

func extractTar(dest string, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		default:
			return fmt.Errorf("unsupported tar entry type %d for %s", hdr.Typeflag, hdr.Name)
		}
	}
}
