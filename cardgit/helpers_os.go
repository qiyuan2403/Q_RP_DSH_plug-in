package cardgit

import "os"

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func osWriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func osMkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }
