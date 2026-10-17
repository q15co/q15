package memory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// memoryContentRoots are the two content roots the memory surface is scoped
// to, in walk order. Nothing outside them is ever in scope: core/, history/,
// notes/ and cognition/ are never enumerated here.
var memoryContentRoots = []string{semanticDirPath, workingDirPath}

// ListMemoryFiles returns every regular file under the two content roots,
// sorted by repository-relative path. It never reads file contents: sizes
// come from the walk's own stat results. Directories and non-regular entries
// such as symlinks are not files and are skipped; a content root that does
// not exist lists as empty. A path that cannot be stat'ed mid-walk fails the
// listing rather than being silently dropped, so the read-only views built on
// it (the status report's surface section, the semantic job's surface
// trigger) never report a partial scan as clean.
func (s *Store) ListMemoryFiles(ctx context.Context) ([]cognition.MemoryFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release := s.repository.Acquire()
	defer release()

	var files []cognition.MemoryFile
	for _, contentRoot := range memoryContentRoots {
		rootPath := filepath.Join(s.root(), filepath.FromSlash(contentRoot))
		err := filepath.WalkDir(
			rootPath,
			func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !entry.Type().IsRegular() {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					if os.IsNotExist(err) {
						// Removed between the readdir and the stat: it is
						// not part of the surface either way.
						return nil
					}
					return fmt.Errorf("stat memory file %q: %w", path, err)
				}
				relative, err := filepath.Rel(s.root(), path)
				if err != nil {
					return fmt.Errorf("relativize memory file %q: %w", path, err)
				}
				files = append(files, cognition.MemoryFile{
					Path:  filepath.ToSlash(relative),
					Bytes: info.Size(),
				})
				return nil
			},
		)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("list memory files under %q: %w", contentRoot, err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
