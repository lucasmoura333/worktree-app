package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// portAllocator escolhe a porta do docker de cada worktree nova: a primeira
// da faixa que nenhum .env de worktree usa e que ninguém está escutando.
type portAllocator struct {
	mu       sync.Mutex
	reserved map[int]bool
}

func newPortAllocator() *portAllocator { return &portAllocator{reserved: map[int]bool{}} }

func (pa *portAllocator) reset() {
	pa.mu.Lock()
	pa.reserved = map[int]bool{}
	pa.mu.Unlock()
}

func (pa *portAllocator) next(cfg *Config, r *RepoConfig, reserve bool) (int, error) {
	d := r.DockerEnv
	pa.mu.Lock()
	defer pa.mu.Unlock()
	used := usedPorts(cfg)
	for p := d.PortMin; p <= d.PortMax; p++ {
		if used[p] || pa.reserved[p] || portBusy(p) {
			continue
		}
		if reserve {
			pa.reserved[p] = true
		}
		return p, nil
	}
	return 0, fmt.Errorf("nenhuma porta livre entre %d e %d", d.PortMin, d.PortMax)
}

func usedPorts(cfg *Config) map[int]bool {
	used := map[int]bool{}
	for _, r := range cfg.Repos {
		d := r.DockerEnv
		if d == nil {
			continue
		}
		container := cfg.containerPath(&r)
		entries, err := os.ReadDir(container)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(container, e.Name(), filepath.FromSlash(d.File)))
			if err != nil {
				continue
			}
			if v, ok := getEnvValue(string(b), d.PortKey); ok {
				if n, err := strconv.Atoi(v); err == nil {
					used[n] = true
				}
			}
		}
	}
	return used
}

// portBusy testa só em 127.0.0.1: escutar em todas as interfaces faria o
// Windows Firewall perguntar sobre o executável.
func portBusy(p int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	if c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond); err == nil {
		_ = c.Close()
		return true
	}
	return false
}
