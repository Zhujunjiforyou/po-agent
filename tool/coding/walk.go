package coding

import (
	"context"
	"fmt"
	"io/fs"
)

type walkFileFunc func(path string, info fs.FileInfo) (stop bool, err error)

func walkFiles(ctx context.Context, files FileSystem, root string, fn walkFileFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := files.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range sortedEntries(entries) {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := joinWorkspacePath(root, entry.Name())
		// 递归遍历
		if entry.IsDir() {
			if isIgnoredDir(entry.Name()) {
				continue
			}
			if err := walkFiles(ctx, files, name, fn); err != nil {
				return err
			}
			continue
		}
		// 递归遍历时不跟随 symlink entry。单次 read 仍可由 os.Root 安全地访问
		// Workspace 内部合法 symlink，但目录 walker 不应该因此产生循环或重复扫描整棵树。
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", displayPath(name), err)
		}
		stop, err := fn(name, info)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}
