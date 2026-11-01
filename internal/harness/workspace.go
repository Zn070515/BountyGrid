package harness

import (
	"fmt"
	"io/fs"
	"os"
)

// Workspace confines file tools to one directory tree using os.Root. Paths that
// escape the root (including symlinks pointing outside it) are rejected by the
// operating system boundary, not by string prefix checks.
type Workspace struct {
	root *os.Root
}

func OpenWorkspace(dir string) (*Workspace, error) {
	if dir == "" {
		return nil, fmt.Errorf("workspace: directory is empty")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: open %s: %w", dir, err)
	}
	return &Workspace{root: root}, nil
}

func (w *Workspace) Close() error {
	if w == nil || w.root == nil {
		return nil
	}
	return w.root.Close()
}

func (w *Workspace) Name() string {
	if w == nil || w.root == nil {
		return ""
	}
	return w.root.Name()
}

func (w *Workspace) ReadFile(name string) ([]byte, error) {
	if w == nil || w.root == nil {
		return nil, fmt.Errorf("workspace: not open")
	}
	return w.root.ReadFile(name)
}

func (w *Workspace) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if w == nil || w.root == nil {
		return fmt.Errorf("workspace: not open")
	}
	return w.root.WriteFile(name, data, perm)
}

func (w *Workspace) Open(name string) (*os.File, error) {
	if w == nil || w.root == nil {
		return nil, fmt.Errorf("workspace: not open")
	}
	return w.root.Open(name)
}

func (w *Workspace) Create(name string) (*os.File, error) {
	if w == nil || w.root == nil {
		return nil, fmt.Errorf("workspace: not open")
	}
	return w.root.Create(name)
}

func (w *Workspace) MkdirAll(name string, perm fs.FileMode) error {
	if w == nil || w.root == nil {
		return fmt.Errorf("workspace: not open")
	}
	return w.root.MkdirAll(name, perm)
}

func (w *Workspace) Remove(name string) error {
	if w == nil || w.root == nil {
		return fmt.Errorf("workspace: not open")
	}
	return w.root.Remove(name)
}
