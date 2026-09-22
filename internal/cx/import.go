package cx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type importSummary struct {
	Copied  int
	Skipped int
}

func (a *App) importProfile(args []string) error {
	if len(args) != 1 {
		return ExitError{Code: 2, Err: errors.New("import requires exactly one profile name")}
	}
	profile, err := a.profilePath(args[0])
	if err != nil {
		return err
	}
	if info, err := os.Stat(profile); err != nil || !info.IsDir() {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("profile %q does not exist; create it with 'cx login %s'", args[0], args[0])
		}
		if err != nil {
			return fmt.Errorf("inspect profile %q: %w", args[0], err)
		}
		return fmt.Errorf("profile path for %q is not a directory", args[0])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	summary, err := importUsageLogs(filepath.Join(home, ".codex"), profile)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "Imported %d usage log(s) into %s", summary.Copied, args[0])
	if summary.Skipped > 0 {
		_, err = fmt.Fprintf(a.Stdout, "; skipped %d existing log(s)", summary.Skipped)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, ".")
	return err
}

func importUsageLogs(sourceHome, profile string) (importSummary, error) {
	source := filepath.Join(sourceHome, "sessions")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return importSummary{}, fmt.Errorf("source usage directory does not exist: %s", source)
	} else if err != nil {
		return importSummary{}, fmt.Errorf("inspect source usage directory: %w", err)
	}
	destination := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return importSummary{}, fmt.Errorf("create destination sessions directory: %w", err)
	}

	var summary importSummary
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if _, err := os.Stat(target); err == nil {
			summary.Skipped++
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = in.Close()
			if errors.Is(err, os.ErrExist) {
				summary.Skipped++
				return nil
			}
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		summary.Copied++
		return nil
	})
	if err != nil {
		return importSummary{}, fmt.Errorf("import usage logs: %w", err)
	}
	return summary, nil
}
