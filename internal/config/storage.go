package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/voidstarit/kouja/internal/fsutil"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

var (
	ErrLocked        = errors.New("config: configuration lock is busy")
	ErrClassNotFound = errors.New("config: storage class not found")
	ErrUnsafePath    = errors.New("config: configuration path is not a safe regular file")
	ErrClassConflict = errors.New("config: storage class maps to an already used VG")
)

// SetClass updates storage.classes in the YAML file under the configuration
// lock, preserves the previous file as .bak, validates the result, and writes
// atomically. It returns non-fatal warning codes such as durability_uncertain.
func SetClass(path, class, vg string) ([]string, error) {
	return updateClass(path, class, vg, false)
}

// RemoveClass removes a class mapping from storage.classes. A missing class is
// reported as ErrClassNotFound.
func RemoveClass(path, class string) ([]string, error) {
	return updateClass(path, class, "", true)
}

func updateClass(path, class, vg string, remove bool) ([]string, error) {
	if path == "" || class == "" || (!remove && vg == "") {
		return nil, fmt.Errorf("%w: path, class, and vg are required", ErrInvalid)
	}
	if err := rejectUnsafePath(path); err != nil {
		return nil, err
	}
	lock, err := acquireConfigLock(path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer releaseConfigLock(lock)

	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied configuration path
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	current, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if remove {
		if _, ok := current.Storage.Classes[class]; !ok {
			return nil, ErrClassNotFound
		}
	} else {
		for otherClass, otherVG := range current.Storage.Classes {
			if otherClass != class && otherVG == vg {
				return nil, fmt.Errorf("%w: class %q already maps to VG %q", ErrClassConflict, otherClass, vg)
			}
		}
		if !vgNamePattern.MatchString(class) || !vgNamePattern.MatchString(vg) {
			return nil, fmt.Errorf("%w: invalid class or VG name", ErrInvalid)
		}
	}

	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("config: parse YAML node: %w", err)
	}
	if err := mutateClassNode(&document, class, vg, remove); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(&document)
	if err != nil {
		return nil, fmt.Errorf("config: marshal updated YAML: %w", err)
	}
	if _, err := Parse(encoded); err != nil {
		return nil, err
	}

	if err := fsutil.WriteFileAtomic(path+".bak", raw, 0o600); err != nil {
		return nil, fmt.Errorf("config: write backup: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, encoded, 0o600); err != nil {
		if errors.Is(err, fsutil.ErrDurabilityUncertain) {
			return []string{"durability_uncertain"}, nil
		}
		return nil, err
	}
	return nil, nil
}

func rejectUnsafePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("config: inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func acquireConfigLock(path string, timeout time.Duration) (*os.File, error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // derived from the configuration path
		if err != nil {
			return nil, fmt.Errorf("config: open lock: %w", err)
		}
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return file, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("config: lock: %w", err)
		}
		_ = file.Close()
		if time.Now().After(deadline) {
			return nil, ErrLocked
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func releaseConfigLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func mutateClassNode(document *yaml.Node, class, vg string, remove bool) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return fmt.Errorf("%w: empty YAML document", ErrInvalid)
	}
	root := document.Content[0]
	storage := mappingValue(root, "storage")
	if storage == nil {
		if remove {
			return ErrClassNotFound
		}
		storage = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "storage"}, storage)
	}
	classes := mappingValue(storage, "classes")
	if classes == nil {
		if remove {
			return ErrClassNotFound
		}
		classes = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		storage.Content = append(storage.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "classes"}, classes)
	}
	if classes.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: storage.classes must be a mapping", ErrInvalid)
	}
	for index := 0; index+1 < len(classes.Content); index += 2 {
		if classes.Content[index].Value != class {
			continue
		}
		if remove {
			classes.Content = append(classes.Content[:index], classes.Content[index+2:]...)
			return nil
		}
		classes.Content[index+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: vg}
		return nil
	}
	if remove {
		return ErrClassNotFound
	}
	classes.Content = append(classes.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: class},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: vg})
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}
