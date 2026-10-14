package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// archiveDirPath is the directory holding the verbatim pre-demotion
	// copies of working memory that the migration folds once, then deletes.
	archiveDirPath = workingDirPath + "/archive"
	// archiveResidueRelativePath is the staged residue of the archive fold:
	// the archive lines not already present in current working memory, walked
	// by the migration as its fourth source before the directory is deleted.
	archiveResidueRelativePath = workingDirPath + "/archive-residue.md"
)

// ArchiveFile is one archive copy read for the fold, keyed by its
// repository-relative slash path.
type ArchiveFile struct {
	RelativePath string
	Content      string
}

// BuildArchiveResidue returns the residue of folding the archive copies into
// the current working memory: every non-blank archive line whose trimmed text
// does not appear, trimmed, among the working memory's lines. Archive order is
// preserved (files in the order given, lines in file order), a line repeated
// across archive files collapses to one residue line, and blank lines are
// dropped.
func BuildArchiveResidue(files []ArchiveFile, workingMemory string) string {
	working := workingMemoryLineSet(workingMemory)
	seen := make(map[string]struct{})
	var builder strings.Builder
	for _, file := range files {
		for _, line := range strings.Split(file.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if _, present := working[trimmed]; present {
				continue
			}
			if _, dup := seen[trimmed]; dup {
				continue
			}
			seen[trimmed] = struct{}{}
			builder.WriteString(trimmed)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// workingMemoryLineSet returns the set of trimmed non-blank lines of working
// memory, the membership test the residue uses.
func workingMemoryLineSet(content string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

// ArchiveFoldSummary reports what one archive fold staged.
type ArchiveFoldSummary struct {
	// FilesFolded is how many working/archive/*.md copies were folded.
	FilesFolded int
	// ResidueLines is how many residue lines the fold produced.
	ResidueLines int
}

// archiveFiles lists the working/archive/*.md copies in path order. A missing
// archive directory yields no files and no error.
func (s *Store) archiveFiles() ([]ArchiveFile, error) {
	dir := filepath.Join(s.root(), filepath.FromSlash(archiveDirPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list working archive %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	files := make([]ArchiveFile, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read archive copy %q: %w", path, err)
		}
		files = append(files, ArchiveFile{
			RelativePath: archiveDirPath + "/" + name,
			Content:      string(raw),
		})
	}
	return files, nil
}

// StageArchiveResidue reads the working/archive/*.md copies, computes the
// residue against workingMemory, and writes it to working/archive-residue.md.
// It returns the fold summary and whether the archive held at least one .md
// file to fold. An absent or empty archive directory is a no-op: the second
// return is false and nothing is written.
func (s *Store) StageArchiveResidue(
	ctx context.Context,
	workingMemory string,
) (ArchiveFoldSummary, bool, error) {
	release := s.repository.Acquire()
	defer release()

	files, err := s.archiveFiles()
	if err != nil {
		return ArchiveFoldSummary{}, false, err
	}
	if len(files) == 0 {
		return ArchiveFoldSummary{}, false, nil
	}
	residue := BuildArchiveResidue(files, workingMemory)
	path := filepath.Join(s.root(), filepath.FromSlash(archiveResidueRelativePath))
	if err := writeTextFileAtomic(path, residue); err != nil {
		return ArchiveFoldSummary{}, false, fmt.Errorf(
			"write archive residue %q: %w",
			path,
			err,
		)
	}
	if err := s.repository.Commit(
		ctx,
		"memory: stage archive fold residue",
		archiveResidueRelativePath,
	); err != nil {
		return ArchiveFoldSummary{}, false, fmt.Errorf(
			"commit archive residue: %w",
			err,
		)
	}
	return ArchiveFoldSummary{
		FilesFolded:  len(files),
		ResidueLines: strings.Count(residue, "\n"),
	}, true, nil
}

// DeleteWorkingArchive removes working/archive-residue.md and the whole
// working/archive/ directory, returning how many archive .md files were folded
// away. A missing directory counts as zero files, so finishing a migration with
// no archive is a no-op. Both paths are resolved under the memory root and
// validated to stay inside it before any removal runs.
func (s *Store) DeleteWorkingArchive(ctx context.Context) (int, error) {
	release := s.repository.Acquire()
	defer release()

	root := s.root()
	archiveDir := filepath.Join(root, filepath.FromSlash(archiveDirPath))
	residuePath := filepath.Join(root, filepath.FromSlash(archiveResidueRelativePath))
	if !pathWithinRoot(root, archiveDir) {
		return 0, fmt.Errorf(
			"working archive dir %q escapes the memory root",
			archiveDirPath,
		)
	}
	if !pathWithinRoot(root, residuePath) {
		return 0, fmt.Errorf(
			"archive residue path %q escapes the memory root",
			archiveResidueRelativePath,
		)
	}

	filesFolded := 0
	if entries, err := os.ReadDir(archiveDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				filesFolded++
			}
		}
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("list working archive %q: %w", archiveDir, err)
	}

	if err := os.Remove(residuePath); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("delete archive residue %q: %w", residuePath, err)
	}
	if err := os.RemoveAll(archiveDir); err != nil {
		return 0, fmt.Errorf("delete working archive %q: %w", archiveDir, err)
	}
	if err := s.repository.Commit(
		ctx,
		"memory: delete working archive",
		archiveResidueRelativePath,
		archiveDirPath,
	); err != nil {
		return 0, fmt.Errorf("commit working archive deletion: %w", err)
	}
	return filesFolded, nil
}

// pathWithinRoot reports whether path is the root or a descendant of it.
func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
