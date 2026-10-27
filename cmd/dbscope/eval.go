package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dbscope/dbscope/internal/eval"
	"github.com/dbscope/dbscope/internal/infer"
	"github.com/dbscope/dbscope/internal/introspect"
	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/store"
)

const evalUsage = `usage:
  dbscope eval nl --dataset DIR --dsn URL [--provider anthropic|openai_compat --model M --base-url U]
                  [--api-key-env NAME] [--limit N] [--json] [--gold-only]

Runs every question in DIR/questions.yaml through the NL pipeline and scores the
answers against the gold SQL, both executed read-only against --dsn.
Without --provider the saved LLM settings are used. The API key comes from the
secrets vault, or from the environment variable named by --api-key-env.
--gold-only runs the gold queries alone (no model calls) and prints their
row counts and checksums.
`

func evalCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "nl" {
		fmt.Fprint(stderr, evalUsage)
		return 2
	}
	flags := flag.NewFlagSet("eval nl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataset := flags.String("dataset", "", "dataset directory (questions.yaml, optional glossary.yaml)")
	dsn := flags.String("dsn", "", "postgres:// URL of the database to evaluate against")
	provider := flags.String("provider", "", "anthropic or openai_compat (default: saved settings)")
	model := flags.String("model", "", "generation model")
	baseURL := flags.String("base-url", "", "OpenAI-compatible base URL, e.g. http://localhost:11434/v1/")
	effort := flags.String("effort", "", "anthropic effort: low, medium or high")
	keyEnv := flags.String("api-key-env", "", "name of an environment variable holding the API key")
	limit := flags.Int("limit", 0, "run only the first N questions")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	goldOnly := flags.Bool("gold-only", false, "only execute the gold SQL; no model calls")
	timeout := flags.Duration("statement-timeout", eval.DefaultStatementTimeout, "per-statement timeout")
	flags.Usage = func() { fmt.Fprint(stderr, evalUsage) }
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *dataset == "" || *dsn == "" || flags.NArg() > 0 || *limit < 0 {
		fmt.Fprint(stderr, evalUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runEval(ctx, evalOptions{
		dataset: *dataset, dsn: *dsn, provider: *provider, model: *model, baseURL: *baseURL, effort: *effort,
		keyEnv: *keyEnv, limit: *limit, json: *asJSON, goldOnly: *goldOnly, timeout: *timeout,
	}, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "dbscope eval: %v\n", err)
		return 1
	}
	return 0
}

type evalOptions struct {
	dataset, dsn, provider, model, baseURL, effort, keyEnv string
	limit                                                  int
	json, goldOnly                                         bool
	timeout                                                time.Duration
}

func runEval(ctx context.Context, o evalOptions, stdout, stderr io.Writer) error {
	ds, err := eval.Load(o.dataset)
	if err != nil {
		return err
	}
	pc, err := pgxpool.ParseConfig(o.dsn)
	if err != nil {
		return errors.New("--dsn does not parse as a PostgreSQL URL") // never echo the DSN
	}
	pc.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
	pc.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return err
	}
	defer pool.Close()
	cfg := eval.Config{Dataset: ds, DB: pool, Limit: o.limit, StatementTimeout: o.timeout}

	if o.goldOnly {
		gold := eval.RunGold(ctx, cfg)
		failed := 0
		for _, g := range gold {
			if g.Error != "" {
				failed++
			}
		}
		if o.json {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(gold); err != nil {
				return err
			}
		} else {
			for _, g := range gold {
				if g.Error != "" {
					fmt.Fprintf(stdout, "%-8s ERROR %s (rows=%d checksum=%s)\n", g.ID, g.Error, g.Rows, g.Checksum)
				} else {
					fmt.Fprintf(stdout, "%-8s rows=%-6d checksum=%s ordered=%v\n", g.ID, g.Rows, g.Checksum, g.Ordered)
				}
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d gold queries failed", failed, len(gold))
		}
		return nil
	}

	p, err := evalProvider(o)
	if err != nil {
		return err
	}
	m, err := introspect.Introspect(ctx, pool, ds.Schemas, o.timeout)
	if err != nil {
		return fmt.Errorf("introspect: %w", err)
	}
	infer.Apply(m, nil)
	cfg.Model, cfg.Provider = m, p
	cfg.Retrieve = retrieve.LocalOptions()
	if p.Name() == llm.NameAnthropic {
		cfg.Retrieve = retrieve.AnthropicOptions()
	}
	if !o.json {
		cfg.Progress = func(q eval.QuestionResult) {
			verdict := "-"
			switch {
			case q.GoldError != "":
				verdict = "GOLD ERROR"
			case q.Match != nil && q.Match.Set:
				verdict = "set-match"
			case q.Expected == "clarify" && q.Kind == "clarify":
				verdict = "clarified"
			case q.Error != "":
				verdict = "error"
			case q.Match != nil:
				verdict = "mismatch"
			}
			fmt.Fprintf(stderr, "%-8s %-12s %-10s %6dms\n", q.ID, q.Category, verdict, q.LatencyMs)
		}
	}
	rep, err := eval.Run(ctx, cfg)
	if err != nil {
		return err
	}
	if o.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(stdout, rep)
	return nil
}

func printReport(w io.Writer, rep *eval.Report) {
	m := rep.Metrics
	pct := func(f float64) string { return fmt.Sprintf("%5.1f%%", f*100) }
	fmt.Fprintf(w, "dataset %s, provider %s, model %s\n", rep.Dataset, rep.Provider, rep.Model)
	fmt.Fprintf(w, "questions          %d (answerable %d, ambiguous %d, unanswerable %d)\n", m.Questions, m.Answerable, m.Ambiguous, m.Unanswerable)
	fmt.Fprintf(w, "Set-EX             %s\n", pct(m.SetEX))
	fmt.Fprintf(w, "Multiset-EX        %s\n", pct(m.MultisetEX))
	fmt.Fprintf(w, "ordered match      %s of %d with ORDER BY\n", pct(m.OrderedMatch), m.Ordered)
	fmt.Fprintf(w, "clarify precision  %s, recall %s\n", pct(m.ClarifyPrecision), pct(m.ClarifyRecall))
	fmt.Fprintf(w, "unanswerable       %d of %d abstained\n", m.Abstained, m.Unanswerable)
	fmt.Fprintf(w, "guard rejections   %d\n", m.GuardRejections)
	fmt.Fprintf(w, "errors             %d (gold errors %d)\n", m.Errors, m.GoldErrors)
	fmt.Fprintf(w, "median latency     %d ms\n", m.MedianLatencyMs)
	fmt.Fprintf(w, "tokens             in %d, out %d, cache read %d\n", m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CacheReadTokens)
	for _, c := range m.Categories() {
		fmt.Fprintf(w, "  %-16s Set-EX %s\n", c, pct(m.SetEXByCategory[c]))
	}
}

// evalProvider builds the provider from flags or the saved settings. The
// API key comes from the variable named by --api-key-env or the vault;
// ANTHROPIC_API_KEY and friends are never read implicitly.
func evalProvider(o evalOptions) (llm.Provider, error) {
	settings := store.LLMSettings{Provider: o.provider, Model: o.model, BaseURL: o.baseURL, Effort: o.effort}
	needSettings := o.provider == ""
	needVault := o.keyEnv == ""
	dir, err := configDir()
	if err != nil && (needSettings || needVault) {
		return nil, err
	}
	if needSettings {
		st, err := store.Open(filepath.Join(dir, "dbscope.db"))
		if err != nil {
			return nil, err
		}
		saved, err := st.GetLLMSettings(context.Background())
		st.Close()
		if err != nil {
			return nil, err
		}
		if saved.Provider == store.ProviderNone {
			return nil, errors.New("no LLM provider is configured; pass --provider or configure one in the UI")
		}
		settings = saved
		if o.model != "" {
			settings.Model = o.model
		}
		if o.baseURL != "" {
			settings.BaseURL = o.baseURL
		}
		if o.effort != "" {
			settings.Effort = o.effort
		}
	}
	var key string
	if o.keyEnv != "" {
		if strings.ContainsAny(o.keyEnv, "= \t") {
			return nil, errors.New("--api-key-env takes a variable name, not a value")
		}
		key = os.Getenv(o.keyEnv)
		if key == "" {
			return nil, fmt.Errorf("environment variable %s is empty", o.keyEnv)
		}
	} else {
		v, err := secrets.Open(filepath.Join(dir, "secrets.age"))
		if err != nil {
			return nil, err
		}
		name := "llm:" + settings.Provider
		k, ok, err := v.Get(name)
		switch {
		// Local OpenAI-compatible servers (Ollama, LM Studio) need no key; a
		// server that does will answer 401, which the eval reports.
		case errors.Is(err, secrets.ErrLocked) && settings.Provider != llm.NameAnthropic:
		case errors.Is(err, secrets.ErrLocked):
			return nil, errors.New("the secrets vault is locked (passphrase backend); pass --api-key-env NAME")
		case err != nil:
			return nil, err
		case !ok && settings.Provider == llm.NameAnthropic:
			return nil, errors.New("no Anthropic API key in the vault; add one in the UI or pass --api-key-env NAME")
		}
		key = k
	}
	if settings.Provider == llm.NameAnthropic {
		if settings.BaseURL != "" {
			return nil, errors.New("--base-url applies to openai_compat only")
		}
		if settings.Model == "" {
			settings.Model = llm.DefaultAnthropicModel
		}
	}
	return llm.New(settings.Provider, llm.Config{APIKey: key, Model: settings.Model, SummaryModel: settings.SummaryModel,
		BaseURL: settings.BaseURL, Effort: settings.Effort})
}

func configDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "dbscope"), nil
}
