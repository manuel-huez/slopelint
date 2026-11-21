package lint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	codexEnv          = "SLOPELINT_CODEX_DESCRIPTIONS"
	codexBatchRecords = 16
	codexBatchBytes   = 64000
	codexBatchPadding = 32
	codexSchemaMode   = 0o600
	codexWorkers      = 4
	codexTimeout      = 10 * time.Minute
	codexAttempts     = 2
)

// codexCLI runs structured, tool-free Codex calls for descriptions and review.
type codexCLI struct {
	path string
}

// codexTask is one structured Codex job shape. Prompts carry batch-local ids;
// decode maps them back to request ids and returns the valid subset with an
// error for the rest, so completed records survive a retry.
type codexTask[Req, Res any] struct {
	name      string
	model     string
	effort    string
	prompt    func([]Req) (string, string, error)
	decode    func([]byte, []Req) ([]Res, error)
	requestID func(Req) string
	resultID  func(Res) string
	// done reports whether another run already stored req's result, and then
	// records it, so runs that overlap do not pay for the same request twice.
	// It is checked before each batch, not only when a run starts.
	done func(Req) bool
}

// finishedIn returns " in 12m3s" once done reaches total, so the last
// progress line of a step says how long it took.
func finishedIn(done, total int, started time.Time) string {
	if done < total {
		return ""
	}

	return " in " + time.Since(started).Round(time.Second).String()
}

// codexEnabledFromEnv reports whether SLOPELINT_CODEX_DESCRIPTIONS allows the
// Codex channel. It does not check whether the CLI is installed.
func codexEnabledFromEnv() (bool, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(codexEnv)))
	if mode != "" && mode != "auto" && mode != offText {
		return false, fmt.Errorf("%s must be auto or off; got %q", codexEnv, mode)
	}

	return mode != offText, nil
}

func (task codexTask[Req, Res]) run(
	ctx context.Context,
	cli *codexCLI,
	batches [][]Req,
	accept func([]Res) error,
) error {
	if cli == nil || cli.path == "" {
		return errors.New("codex CLI is unavailable")
	}

	errs := make([]error, len(batches))
	jobs := make(chan int)
	// Codex requests wait on service inference. Bound their process count
	// separately from the CPU budget used by local embedding inference.
	workerCount := min(codexWorkers, len(batches))

	var workers sync.WaitGroup
	for range workerCount {
		workers.Go(func() {
			for index := range jobs {
				errs[index] = task.runBatch(ctx, cli.path, batches[index], accept)
			}
		})
	}

	// Stop handing out batches once the scan fails or the command is stopped.
	// Cancellation also kills running Codex processes in executeCodex.
queue:
	for index := range batches {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break queue
		}
	}

	close(jobs)
	workers.Wait()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex %s stopped: %w", task.name, err)
	}

	for index, err := range errs {
		if err != nil {
			return fmt.Errorf("codex %s batch %d/%d: %w", task.name, index+1, len(batches), err)
		}
	}

	return nil
}

func (task codexTask[Req, Res]) runBatch(
	ctx context.Context,
	path string,
	remaining []Req,
	accept func([]Res) error,
) error {
	var lastErr error

	failedAttempts := 0

	for ctx.Err() == nil {
		if remaining = task.unfinished(remaining, nil); len(remaining) == 0 {
			return nil
		}

		result, err := task.call(ctx, path, remaining)

		// Persist every valid subset before retrying missing records. A slow or
		// failed retry must not discard completed inference work.
		if len(result) > 0 {
			if acceptErr := accept(result); acceptErr != nil {
				return acceptErr
			}
		}

		if err == nil {
			return nil
		}

		lastErr = err

		if len(result) > 0 {
			failedAttempts = 0
			remaining = task.unfinished(remaining, result)

			continue
		}

		if failedAttempts++; failedAttempts == codexAttempts {
			return lastErr
		}
	}

	return lastErr
}

func (task codexTask[Req, Res]) unfinished(remaining []Req, result []Res) []Req {
	returned := make(map[string]bool, len(result))
	for _, record := range result {
		returned[task.resultID(record)] = true
	}

	return slices.DeleteFunc(slices.Clone(remaining), func(request Req) bool {
		return returned[task.requestID(request)] || task.done != nil && task.done(request)
	})
}

func (task codexTask[Req, Res]) call(
	ctx context.Context,
	path string,
	batch []Req,
) ([]Res, error) {
	prompt, schema, err := task.prompt(batch)
	if err != nil {
		return nil, err
	}

	data, err := executeCodex(ctx, path, task.model, task.effort, prompt, schema)
	if err != nil {
		return nil, err
	}

	return task.decode(data, batch)
}

func executeCodex(
	ctx context.Context,
	path, model, effort, prompt, schema string,
) ([]byte, error) {
	dir, err := os.MkdirTemp("", "slopelint-codex-*")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	schemaPath := dir + "/schema.json"
	outputPath := dir + "/output.json"

	if err := os.WriteFile(schemaPath, []byte(schema), codexSchemaMode); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, codexTimeout)
	defer cancel()

	command := exec.CommandContext(
		ctx,
		path,
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--color", "never",
		"--disable", "plugins",
		"--disable", "apps",
		"--disable", "shell_tool",
		"--disable", "code_mode_host",
		"--disable", "multi_agent",
		"--model", model,
		"-c", `model_reasoning_effort="`+effort+`"`,
		"--output-schema", schemaPath,
		"--output-last-message", outputPath,
		"-",
	)
	command.Dir = dir
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = io.Discard
	command.Stderr = io.Discard

	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("codex CLI timed out after %s", codexTimeout)
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf("codex CLI failed: %w", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read codex output: %w", err)
	}

	return data, nil
}

// balanceCodexBatches splits one prompt shape into batches. Requests must be
// sorted longest first: each goes to the least loaded batch within record and
// byte limits, and one oversized request stays intact in its own batch.
func balanceCodexBatches[Req any](requests []Req, size func(Req) int) [][]Req {
	count := (len(requests) + codexBatchRecords - 1) / codexBatchRecords
	batches := make([][]Req, count)
	sizes := make([]int, count)

	for _, request := range requests {
		requestSize := size(request) + codexBatchPadding
		selected := -1

		for index, batch := range batches {
			if len(batch) >= codexBatchRecords ||
				(len(batch) > 0 && sizes[index]+requestSize > codexBatchBytes) {
				continue
			}

			if selected < 0 || sizes[index] < sizes[selected] {
				selected = index
			}
		}

		if selected < 0 {
			selected = len(batches)
			batches = append(batches, nil)
			sizes = append(sizes, 0)
		}

		batches[selected] = append(batches[selected], request)
		sizes[selected] += requestSize
	}

	return batches
}

func decodeCodexOutput(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode codex output: %w", err)
	}

	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("decode codex output: trailing JSON content")
	}

	return nil
}

func codexBatchID(index int) string {
	return fmt.Sprintf("b%04d", index+1)
}
