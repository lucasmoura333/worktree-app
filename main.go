// worktree-app cria e remove worktrees de vários repos por uma tela local:
// worktree em cada repo escolhido, arquivos não versionados, deps, porta do
// docker, pasta de tasks e .code-workspace.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func main() {
	// "hook" roda dentro dos agentes: sem config, sem saída, sempre 0.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHook(os.Args[2:], os.Stdin, time.Now())
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		runShell(os.Args[2:])
		return
	}
	cfgPath := flag.String("config", "", "caminho do config.json (padrão: ao lado do executável)")
	demo := flag.Bool("demo", false, "prévia visual em 127.0.0.1:7788, com exemplos e sem alterar repositórios ou Vault")
	noOpen := flag.Bool("no-open", false, "não abrir o navegador")
	flag.Parse()

	if *cfgPath == "" {
		*cfgPath = defaultConfigPath()
	}
	var cfg *Config
	var err error
	if *demo {
		cfg = &Config{Root: "Prévia visual · dados de exemplo", Listen: "127.0.0.1:7788"}
	} else {
		cfg, err = loadConfig(*cfgPath)
	}
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	url := "http://" + cfg.Listen + "/"
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		// Porta ocupada costuma ser outra instância deste app.
		fmt.Printf("não deu para escutar em %s (%v); abrindo a instância que já está rodando\n", cfg.Listen, err)
		if !*noOpen {
			openBrowser(url)
		}
		return
	}
	handler := newServer(cfg).routes()
	if *demo {
		handler = demoHandler(cfg)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("worktree-app em %s\nroot: %s\nconfig: %s\nCtrl+C encerra\n\n", url, cfg.Root, *cfgPath)
	if !*noOpen {
		openBrowser(url)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func defaultConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "config.json")
		if exists(p) {
			return p
		}
	}
	return "config.json"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
